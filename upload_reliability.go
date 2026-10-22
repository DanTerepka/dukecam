package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
)

// One shared gate bounds combined decoder/thumbnail working memory.
var imageWorkGate = make(chan struct{}, 1)
var projectSlugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
var mediaNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)

func validProjectSlug(slug string) bool {
	return len(slug) <= 100 && projectSlugPattern.MatchString(slug)
}
func validMediaFilename(name string) bool {
	return len(name) <= 500 && mediaNamePattern.MatchString(name) && !strings.Contains(name, "..") && isAllowedExt(strings.ToLower(filepath.Ext(name)))
}

var uploadIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,199}$`)

func validUploadID(id string) bool { return uploadIDPattern.MatchString(id) }

// Decode the entire image, not just a user supplied extension or MIME header.
// HEIC is deliberately rejected in this pilot instead of transcoding evidence.
func validatePilotImage(data []byte) (string, error) {
	imageWorkGate <- struct{}{}
	defer func() { <-imageWorkGate }()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") {
		return "", fmt.Errorf("pilot supports JPEG and PNG only; select Most Compatible on iPhone")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 25_000_000 {
		return "", fmt.Errorf("image dimensions exceed 25 megapixels")
	}
	if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return "", fmt.Errorf("corrupt image")
	}
	if format == "jpeg" {
		return ".jpg", nil
	}
	return ".png", nil
}

// fsync both file and parent before a database receipt can acknowledge an upload.
func saveOriginalAtomic(data []byte, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	// Sync every ancestor, including parents of directories just created by
	// MkdirAll. Syncing only the leaf would not durably link new year/month dirs.
	dirPath, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return err
	}
	for {
		dir, err := os.Open(dirPath)
		if err != nil {
			return err
		}
		syncErr := dir.Sync()
		closeErr := dir.Close()
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		parent := filepath.Dir(dirPath)
		if parent == dirPath {
			break
		}
		dirPath = parent
	}
	return nil
}

func (db *DB) CreateUploadReceiptTables(ctx context.Context) error {
	_, err := db.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS upload_receipts (
 project_id INTEGER NOT NULL REFERENCES projects(id),
 client_upload_id VARCHAR(200) NOT NULL,
 checksum CHAR(64) NOT NULL,
 photo_id INTEGER NOT NULL REFERENCES photos(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(project_id,client_upload_id))`)
	return err
}

func (a *App) storeIdempotentUpload(c echo.Context, project *Project, file *multipart.FileHeader, meta photoMeta) (*Photo, int, string) {
	key := c.FormValue("client_upload_id")
	if !validUploadID(key) {
		return nil, 400, "valid client_upload_id required"
	}
	src, err := file.Open()
	if err != nil {
		return nil, 400, "cannot open file"
	}
	data, err := io.ReadAll(io.LimitReader(src, int64(a.config.MaxUploadMB)*1024*1024+1))
	src.Close()
	if err != nil {
		return nil, 400, "cannot read file"
	}
	if len(data) > a.config.MaxUploadMB*1024*1024 {
		return nil, 413, "file too large"
	}
	hash := sha256.Sum256(data)
	checksum := hex.EncodeToString(hash[:])
	ctx := c.Request().Context()
	tx, err := a.db.pool.Begin(ctx)
	if err != nil {
		return nil, 500, "database unavailable"
	}
	defer tx.Rollback(context.Background())
	// The transaction lock spans lookup, durable file save, photo and receipt insertion.
	// Hash collisions merely serialize unrelated uploads and cannot alias receipts.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("%d:%s", project.ID, key)); err != nil {
		return nil, 500, "database unavailable"
	}
	var photoID int
	var prior string
	err = tx.QueryRow(ctx, `SELECT checksum,photo_id FROM upload_receipts WHERE project_id=$1 AND client_upload_id=$2`, project.ID, key).Scan(&prior, &photoID)
	if err == nil {
		if prior != checksum {
			return nil, http.StatusConflict, "client_upload_id already used for different photo bytes"
		}
		photo, err := getPhotoQuery(ctx, tx, photoID)
		if err != nil {
			return nil, 500, "receipt photo unavailable"
		}
		// Detect missing/corrupted originals; never acknowledge unavailable evidence.
		original, err := os.ReadFile(photo.StoragePath)
		if err != nil {
			return nil, 500, "stored original unavailable"
		}
		digest := sha256.Sum256(original)
		if hex.EncodeToString(digest[:]) != checksum {
			return nil, 500, "stored original integrity failure"
		}
		return photo, 0, ""
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, 500, "database unavailable"
	}
	meta.uploadTx = tx
	if actor, ok := c.Get("pilot_actor").(string); ok && actor != "" {
		meta.WorkerID = nil
		meta.WorkerName = actor
	}
	photo, status, msg := a.storeUploadedPhoto(c, project, file, meta)
	if status != 0 {
		return nil, status, msg
	}
	if _, err = tx.Exec(ctx, `INSERT INTO upload_receipts(project_id,client_upload_id,checksum,photo_id) VALUES($1,$2,$3,$4)`, project.ID, key, checksum, photo.ID); err != nil {
		os.Remove(photo.StoragePath)
		return nil, 500, "receipt save failed"
	}
	// On ambiguous commit failure retain the file: retry resolves the receipt and
	// cannot lose an original when PostgreSQL committed but its reply was lost.
	if err = tx.Commit(ctx); err != nil {
		return nil, 500, "upload acknowledgment unavailable; retry the same upload"
	}
	return photo, 0, ""
}
