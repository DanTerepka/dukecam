package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"hash/crc32"
	"image"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUploadIDValidation(t *testing.T) {
	for _, s := range []string{"a", "queue-123_456.abc"} {
		if !validUploadID(s) {
			t.Errorf("valid ID rejected: %q", s)
		}
	}
	for _, s := range []string{"", "../x", "a/b", "with space", string(bytes.Repeat([]byte("x"), 201))} {
		if validUploadID(s) {
			t.Errorf("invalid ID accepted: %q", s)
		}
	}
}
func TestValidatePilotImage(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3)))
	ext, err := validatePilotImage(b.Bytes())
	if err != nil || ext != ".png" {
		t.Fatalf("valid PNG: %s %v", ext, err)
	}
	for _, data := range [][]byte{[]byte("not an image"), b.Bytes()[:30], []byte("0000ftypheic")} {
		if _, err := validatePilotImage(data); err == nil {
			t.Fatal("invalid or unsupported image accepted")
		}
	}
}
func TestAtomicOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "original.png")
	data := []byte("original bytes")
	if err := saveOriginalAtomic(data, path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("original mismatch", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatal("temporary file leaked")
	}
}

// TEST_DATABASE_URL must name an isolated disposable pilot/test database.
func TestUploadPostgresRetryAndConcurrency(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for real PostgreSQL upload checks")
	}
	ctx := context.Background()
	db, err := NewDB(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.CreateTables(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateUploadReceiptTables(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "Retry test", "test-"+uuid.NewString(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.pool.Exec(ctx, `DELETE FROM upload_receipts WHERE project_id=$1`, project.ID)
	defer func() {
		db.pool.Exec(ctx, `DELETE FROM upload_receipts WHERE project_id=$1`, project.ID)
		db.pool.Exec(ctx, `DELETE FROM photos WHERE project_id=$1`, project.ID)
		db.pool.Exec(ctx, `DELETE FROM projects WHERE id=$1`, project.ID)
	}()
	root := t.TempDir()
	app := &App{db: db, config: Config{MaxUploadMB: 2, StoragePath: filepath.Join(root, "photos"), ThumbPath: filepath.Join(root, "thumbs")}}
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3)))
	data := b.Bytes()
	upload := func(key string, content []byte) (int, map[string]any) {
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		w.WriteField("project_id", strconv.Itoa(project.ID))
		w.WriteField("client_upload_id", key)
		f, _ := w.CreateFormFile("file", "evidence.png")
		f.Write(content)
		w.Close()
		req := httptest.NewRequest("POST", "/api/upload", &body)
		req.Header.Set("Content-Type", w.FormDataContentType())
		rec := httptest.NewRecorder()
		e := echo.New()
		c := e.NewContext(req, rec)
		c.Set("pilot_actor", "verified@example.test")
		if err := app.UploadPhoto(c); err != nil {
			e.HTTPErrorHandler(err, c)
		}
		var result map[string]any
		json.Unmarshal(rec.Body.Bytes(), &result)
		return rec.Code, result
	}
	if code, _ := upload("", data); code != 400 {
		t.Fatalf("missing key: %d", code)
	}
	if code, _ := upload("invalid-image", []byte("garbage")); code != 400 {
		t.Fatalf("invalid image: %d", code)
	}
	var wg sync.WaitGroup
	results := make(chan map[string]any, 16)
	codes := make(chan int, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); code, result := upload("same-photo", data); codes <- code; results <- result }()
	}
	wg.Wait()
	close(codes)
	close(results)
	for code := range codes {
		if code != 200 {
			t.Fatalf("concurrent upload: %d", code)
		}
	}
	var id float64
	for r := range results {
		n, _ := r["id"].(float64)
		if n <= 0 {
			t.Fatalf("bad response %v", r)
		}
		if id != 0 && id != n {
			t.Fatal("duplicate photo rows")
		}
		id = n
		if r["client_upload_id"] != "same-photo" {
			t.Fatal("missing acknowledgment key")
		}
	}
	photo, err := db.GetPhotoByID(ctx, int(id))
	if err != nil {
		t.Fatal(err)
	}
	if photo.WorkerNameOverride == nil || *photo.WorkerNameOverride != "verified@example.test" {
		t.Fatal("lost verified actor")
	}
	got, _ := os.ReadFile(photo.StoragePath)
	if !bytes.Equal(got, data) {
		t.Fatal("original changed")
	}
	var other bytes.Buffer
	png.Encode(&other, image.NewRGBA(image.Rect(0, 0, 3, 3)))
	if code, _ := upload("same-photo", other.Bytes()); code != 409 {
		t.Fatalf("conflicting retry: %d", code)
	}
	// Simulate server restart with a new DB pool; the receipt must survive.
	db2, err := NewDB(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	app.db = db2
	if code, r := upload("same-photo", data); code != 200 || r["id"] != id {
		t.Fatalf("restart retry: %d %v", code, r)
	}
	if err := os.Remove(photo.StoragePath); err != nil {
		t.Fatal(err)
	}
	if code, _ := upload("same-photo", data); code != 500 {
		t.Fatalf("missing evidence acknowledged: %d", code)
	}
}

func TestFindFileRejectsTraversalAndGlobs(t *testing.T) {
	root := t.TempDir()
	name := "43d54b00-8768-4ad0-aeef-12345678.png"
	path := filepath.Join(root, "safe-job", "2026", "10", "22", name)
	if err := saveOriginalAtomic([]byte("test"), path); err != nil {
		t.Fatal(err)
	}
	resolvedPath, _ := filepath.EvalSymlinks(path)
	if got, err := FindFile(root, "safe-job", name); err != nil || got != resolvedPath {
		t.Fatalf("valid file: %q %v", got, err)
	}
	for _, slug := range []string{"..", ".", "safe-job/../safe-job", "*", "safe*", "UPPER", "safe.job"} {
		if _, err := FindFile(root, slug, name); err == nil {
			t.Errorf("unsafe slug accepted: %q", slug)
		}
	}
	for _, name := range []string{"*", "*.png", "../x.png", "a..png", "file.svg", "file[1].png", "file.png/extra"} {
		if _, err := FindFile(root, "safe-job", name); err == nil {
			t.Errorf("unsafe filename accepted: %q", name)
		}
	}
	outside := t.TempDir()
	outsideName := "outside.png"
	os.WriteFile(filepath.Join(outside, outsideName), []byte("outside"), 0600)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := FindFile(root, "escape", outsideName); err == nil {
		t.Fatal("symlink escape allowed")
	}
}
func TestPilotPixelCap(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3)))
	data := b.Bytes()
	binary.BigEndian.PutUint32(data[16:20], 5000)
	binary.BigEndian.PutUint32(data[20:24], 5200)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	if _, err := validatePilotImage(data); err == nil || !strings.Contains(err.Error(), "25 megapixels") {
		t.Fatalf("oversize header not rejected early: %v", err)
	}
}

func TestImageOperationsShareMemoryGate(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3)))
	data := b.Bytes()
	for _, op := range []struct {
		name string
		run  func()
	}{
		{"validation", func() { validatePilotImage(data) }},
		{"processing", func() { ProcessUpload(data) }},
		{"thumbnail", func() { MakeThumbFromBytes(data, filepath.Join(t.TempDir(), "thumb.png"), 0) }},
	} {
		t.Run(op.name, func(t *testing.T) {
			imageWorkGate <- struct{}{}
			done := make(chan struct{})
			go func() { op.run(); close(done) }()
			select {
			case <-done:
				<-imageWorkGate
				t.Fatal("image operation bypassed shared gate")
			case <-time.After(20 * time.Millisecond):
			}
			<-imageWorkGate
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("image operation did not release gate")
			}
		})
	}
}
