package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/adrium/goheif" // registers HEIC/HEIF decoder via image.RegisterFormat
	"github.com/disintegration/imaging"
	"github.com/rwcarlsen/goexif/exif"
)

// EXIFData holds extracted GPS and timestamp from image EXIF.
type EXIFData struct {
	Lat     *float64
	Lng     *float64
	TakenAt *time.Time
}

// ProcessedImage holds the result of inspecting an upload.
type ProcessedImage struct {
	Image     image.Image // only populated when a transcode is required (HEIC/HEIF)
	EXIF      EXIFData
	Width     int
	Height    int
	Processed bool // false if the image couldn't be read at all

	// NeedsTranscode is true for formats browsers can't render (HEIC/HEIF).
	// Those must be re-encoded to JPEG before they're servable; everything
	// else is stored byte-for-byte as uploaded.
	NeedsTranscode bool
}

// ExtractEXIF reads GPS and DateTime from EXIF data.
func ExtractEXIF(data []byte) EXIFData {
	result := EXIFData{}

	x, err := exif.Decode(bytes.NewReader(data))
	if err != nil {
		return result
	}

	// DateTime
	if dt, err := x.DateTime(); err == nil {
		result.TakenAt = &dt
	}

	// GPS
	if lat, lng, err := x.LatLong(); err == nil {
		result.Lat = &lat
		result.Lng = &lng
	}

	return result
}

// ProcessUpload inspects an upload without decoding its pixel data.
//
// This used to fully decode the image and render a thumbnail on the caller's
// goroutine, then re-encode the full-resolution image at q=95 to store it —
// roughly 120ms per megapixel, so ~2.4s of the client's wait for a 24 MP phone
// photo, and a generation of quality lost on every "original". Dimensions now
// come from the image header and thumbnails render asynchronously.
func ProcessUpload(data []byte) (*ProcessedImage, error) {
	imageWorkGate <- struct{}{}
	defer func() { <-imageWorkGate }()
	// EXIF comes from the original bytes, before any transform.
	exifData := ExtractEXIF(data)

	// DecodeConfig reads only the header — microseconds, not milliseconds.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		// Unreadable or an unregistered format — store raw, no thumbnail.
		return &ProcessedImage{Processed: false}, nil
	}

	w, h := cfg.Width, cfg.Height
	// EXIF orientations 5-8 transpose the image, so the displayed dimensions
	// are the reverse of the stored ones.
	if o := exifOrientation(data); o >= 5 && o <= 8 {
		w, h = h, w
	}

	p := &ProcessedImage{EXIF: exifData, Width: w, Height: h, Processed: true}

	// HEIC/HEIF isn't renderable in browsers, so it still needs a real decode
	// and a JPEG re-encode. Rare in practice — 2 of 4,361 stored files.
	if format == "heic" || format == "heif" {
		img, derr := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
		if derr != nil {
			return &ProcessedImage{Processed: false}, nil
		}
		p.Image = img
		p.Width, p.Height = img.Bounds().Dx(), img.Bounds().Dy()
		p.NeedsTranscode = true
	}

	return p, nil
}

// exifOrientation returns the EXIF orientation tag, defaulting to 1 (normal)
// when absent or unreadable.
func exifOrientation(data []byte) int {
	x, err := exif.Decode(bytes.NewReader(data))
	if err != nil {
		return 1
	}
	t, err := x.Get(exif.Orientation)
	if err != nil {
		return 1
	}
	v, err := t.Int(0)
	if err != nil {
		return 1
	}
	return v
}

// MakeThumbFromBytes decodes an upload and writes its 400x400 thumbnail.
// This is the expensive call — run it off the request path via ThumbQueue.
func MakeThumbFromBytes(data []byte, thumbPath string, quality int) error {
	imageWorkGate <- struct{}{}
	defer func() { <-imageWorkGate }()
	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return SaveImage(imaging.Fit(img, 400, 400, imaging.Lanczos), thumbPath, quality)
}

// SaveImage writes an image to disk as JPEG or PNG based on extension.
func SaveImage(img image.Image, path string, quality int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".png" {
		return png.Encode(f, img)
	}
	return jpeg.Encode(f, img, &jpeg.Options{Quality: quality})
}

// SaveRaw writes raw bytes to disk (for formats we can't process).
func SaveRaw(data []byte, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// FindFile locates a file in a date-organized directory tree.
// Path pattern: {base}/{slug}/{YYYY}/{MM}/{DD}/{filename}
func FindFile(basePath, slug, filename string) (string, error) {
	if !validProjectSlug(slug) || !validMediaFilename(filename) {
		return "", fmt.Errorf("invalid media path")
	}
	root, err := filepath.Abs(basePath)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	projectPath := filepath.Join(root, slug)
	info, err := os.Lstat(projectPath)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("invalid project directory")
	}
	found := ""
	err = filepath.WalkDir(projectPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == projectPath {
			return nil
		}
		rel, err := filepath.Rel(projectPath, path)
		if err != nil {
			return err
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			n := len(parts)
			expected := 2
			if n == 1 {
				expected = 4
			}
			if n > 3 || len(entry.Name()) != expected || strings.IndexFunc(entry.Name(), func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
				return filepath.SkipDir
			}
			return nil
		}
		if len(parts) == 4 && entry.Name() == filename && entry.Type().IsRegular() {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			contained, err := filepath.Rel(root, resolved)
			if err != nil || contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
				return fmt.Errorf("media escapes storage root")
			}
			found = resolved
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("file not found")
	}
	return found, nil
}

// RotateFile reads an image from disk, rotates it, saves it back, and regenerates the thumbnail.
// direction: "cw" for 90° clockwise, "ccw" for 90° counter-clockwise, "180" for 180°.
func RotateFile(photoPath, thumbPath, direction string) (int, int, error) {
	img, err := imaging.Open(photoPath)
	if err != nil {
		return 0, 0, fmt.Errorf("open image: %w", err)
	}

	var rotated *image.NRGBA
	switch direction {
	case "cw":
		rotated = imaging.Rotate270(img) // 270° CCW = 90° CW
	case "ccw":
		rotated = imaging.Rotate90(img) // 90° CCW
	case "180":
		rotated = imaging.Rotate180(img)
	default:
		return 0, 0, fmt.Errorf("invalid direction: %s", direction)
	}

	ext := strings.ToLower(filepath.Ext(photoPath))
	quality := 85
	if ext == ".png" {
		quality = 0
	}

	if err := SaveImage(rotated, photoPath, quality); err != nil {
		return 0, 0, fmt.Errorf("save rotated: %w", err)
	}

	// Regenerate thumbnail
	if thumbPath != "" {
		thumb := imaging.Fit(rotated, 400, 400, imaging.Lanczos)
		thumbQuality := 80
		if ext == ".png" {
			thumbQuality = 0
		}
		if err := SaveImage(thumb, thumbPath, thumbQuality); err != nil {
			// Non-fatal
			fmt.Printf("regenerate thumb error: %v\n", err)
		}
	}

	return rotated.Bounds().Dx(), rotated.Bounds().Dy(), nil
}

// EncodeToBytes encodes an image to a byte slice (for QR generation etc).
func EncodeToBytes(img image.Image, format string) ([]byte, error) {
	var buf bytes.Buffer
	var w io.Writer = &buf

	switch format {
	case "png":
		if err := png.Encode(w, img); err != nil {
			return nil, err
		}
	default:
		if err := jpeg.Encode(w, img, &jpeg.Options{Quality: 85}); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}
