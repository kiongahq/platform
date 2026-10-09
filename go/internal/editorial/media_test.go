package editorial

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ml-ai-ops/platform/internal/storage"
)

// withExif inserts an APP1 Exif segment (orientation + a fake GPS marker
// string) right after SOI, the way cameras write it.
func withExif(t *testing.T, data []byte, orientation uint16) []byte {
	t.Helper()
	var tiff bytes.Buffer
	tiff.WriteString("MM")
	_ = binary.Write(&tiff, binary.BigEndian, uint16(42))
	_ = binary.Write(&tiff, binary.BigEndian, uint32(8))
	_ = binary.Write(&tiff, binary.BigEndian, uint16(1))
	_ = binary.Write(&tiff, binary.BigEndian, uint16(0x0112))
	_ = binary.Write(&tiff, binary.BigEndian, uint16(3))
	_ = binary.Write(&tiff, binary.BigEndian, uint32(1))
	_ = binary.Write(&tiff, binary.BigEndian, orientation)
	_ = binary.Write(&tiff, binary.BigEndian, uint16(0))
	_ = binary.Write(&tiff, binary.BigEndian, uint32(0))
	tiff.WriteString("GPSLatitude=51.5074;SerialNumber=CAM-SECRET-123")
	segment := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1}
	out = binary.BigEndian.AppendUint16(out, uint16(len(segment)+2))
	out = append(out, segment...)
	return append(out, data[2:]...)
}

func TestProcessImageStripsExifAndAppliesOrientation(t *testing.T) {
	original := withExif(t, jpegBytes(t, 400, 200), 6)
	if jpegOrientation(original) != 6 || !bytes.Contains(original, []byte("CAM-SECRET-123")) {
		t.Fatal("fixture lacks EXIF")
	}
	processed, err := ProcessImage(original)
	if err != nil {
		t.Fatal(err)
	}
	// Orientation 6 rotates 90° clockwise: 400x200 becomes 200x400.
	if processed.Width != 200 || processed.Height != 400 {
		t.Fatalf("orientation not applied: %dx%d", processed.Width, processed.Height)
	}
	for _, variant := range processed.Variants {
		if bytes.Contains(variant.Data, []byte("Exif")) || bytes.Contains(variant.Data, []byte("CAM-SECRET")) || bytes.Contains(variant.Data, []byte("GPS")) {
			t.Fatalf("variant %s kept metadata", variant.Name)
		}
		if jpegOrientation(variant.Data) != 1 {
			t.Fatal("variant still carries orientation")
		}
	}
}

func TestProcessImageVariantsAndFormats(t *testing.T) {
	processed, err := ProcessImage(jpegBytes(t, 2400, 1200))
	if err != nil {
		t.Fatal(err)
	}
	if len(processed.Variants) != 3 {
		t.Fatalf("variants %d", len(processed.Variants))
	}
	for i, want := range []int{480, 960, 1600} {
		variant := processed.Variants[i]
		decoded, format, err := image.Decode(bytes.NewReader(variant.Data))
		if err != nil || format != "jpeg" || decoded.Bounds().Dx() != want || decoded.Bounds().Dy() != want/2 || variant.Width != want {
			t.Fatalf("variant %d: %v %s %v", want, err, format, decoded.Bounds())
		}
	}
	// Transparent PNG stays PNG with alpha.
	rgba := image.NewNRGBA(image.Rect(0, 0, 600, 300))
	rgba.Set(1, 1, color.NRGBA{255, 0, 0, 128})
	var pngData bytes.Buffer
	_ = png.Encode(&pngData, rgba)
	processed, err = ProcessImage(pngData.Bytes())
	if err != nil || processed.Variants[0].ContentType != "image/png" || processed.SourceType != "image/png" {
		t.Fatalf("png: %+v %v", processed, err)
	}
	// GIF is accepted (first frame).
	var gifData bytes.Buffer
	_ = gif.Encode(&gifData, image.NewPaletted(image.Rect(0, 0, 50, 50), []color.Color{color.Black, color.White}), nil)
	if processed, err = ProcessImage(gifData.Bytes()); err != nil || processed.Width != 50 {
		t.Fatalf("gif: %v", err)
	}
}

func TestProcessImageRejectsWrongMIMEOversizeAndBombs(t *testing.T) {
	cases := map[string][]byte{
		"svg":          []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`),
		"html":         []byte(`<html><script>alert(1)</script></html>`),
		"pdf":          []byte("%PDF-1.4\n%..."),
		"truncated":    jpegBytes(t, 50, 50)[:40],
		"png-renamed":  []byte("not really a png but called photo.png"),
		"polyglot-zip": append([]byte("PK\x03\x04"), make([]byte, 100)...),
	}
	for name, data := range cases {
		if _, err := ProcessImage(data); !errors.Is(err, ErrUnsupportedImage) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ProcessImage(make([]byte, MaxUploadBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
	// A PNG header claiming 20000x20000 is rejected before decoding pixels.
	var header bytes.Buffer
	_ = png.Encode(&header, image.NewGray(image.Rect(0, 0, 1, 1)))
	bomb := header.Bytes()
	binary.BigEndian.PutUint32(bomb[16:20], 20000)
	binary.BigEndian.PutUint32(bomb[20:24], 20000)
	if _, err := ProcessImage(bomb); !errors.Is(err, ErrImageDimensions) && !errors.Is(err, ErrUnsupportedImage) {
		t.Fatalf("bomb: %v", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(jpegBytes(t, 2, 2))); err != nil {
		t.Fatal(err)
	}
}

func TestFSStoreRejectsTraversal(t *testing.T) {
	store := &FSStore{Dir: t.TempDir()}
	for _, key := range []string{"../etc/passwd", "med-abcdef12/../../x", "med-abcdef12/original", "/abs"} {
		if err := store.Put(context.Background(), key, "image/jpeg", []byte("x")); err == nil {
			t.Errorf("key %q accepted", key)
		}
	}
}

// fakeObjectStore is a minimal in-memory S3 that requires presigned URLs.
func fakeObjectStore(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	objects := map[string][]byte{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("X-Amz-Signature") == "" || !strings.HasPrefix(r.URL.Path, "/kionga-blog-media/media/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			objects[r.URL.Path] = body
		case http.MethodGet:
			body, ok := objects[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(body)
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
}

func TestS3StoreRoundTrip(t *testing.T) {
	server := fakeObjectStore(t)
	defer server.Close()
	s3 := &S3Store{Bucket: "kionga-blog-media", Client: server.Client(), Config: storage.Config{Endpoint: server.URL, AccessKey: "k", SecretKey: "s"}}
	ctx := context.Background()
	if err := s3.Put(ctx, "med-abcdef12/480", "image/jpeg", []byte("pixels")); err != nil {
		t.Fatal(err)
	}
	body, err := s3.Get(ctx, "med-abcdef12/480")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(body)
	body.Close()
	if string(data) != "pixels" {
		t.Fatalf("got %q", data)
	}
	if err := s3.Delete(ctx, "med-abcdef12/480"); err != nil {
		t.Fatal(err)
	}
	if _, err := s3.Get(ctx, "med-abcdef12/480"); !errors.Is(err, ErrMediaMissing) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestMediaStoreFromEnv(t *testing.T) {
	t.Setenv("KIONGA_BLOG_MEDIA_BACKEND", "")
	t.Setenv("KIONGA_BLOG_MEDIA_S3_ENDPOINT", "")
	if MediaStoreFromEnv().Name() != "fs" {
		t.Fatal("default backend should be fs without an endpoint")
	}
	t.Setenv("KIONGA_BLOG_MEDIA_S3_ENDPOINT", "http://minio:9000")
	if store := MediaStoreFromEnv(); store.Name() != "s3" || store.(*S3Store).Bucket != "kionga-blog-media" {
		t.Fatal("endpoint should select s3 with the default bucket")
	}
}
