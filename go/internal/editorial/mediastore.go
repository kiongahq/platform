package editorial

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/storage"
)

// MediaStore keeps media bytes. Keys look like "<media id>/<variant>".
type MediaStore interface {
	Name() string
	Put(ctx context.Context, key, contentType string, data []byte) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// ErrMediaMissing reports an absent object.
var ErrMediaMissing = errors.New("media object not found")

var keyPattern = regexp.MustCompile(`^med-[a-z0-9]{6,40}/(480|960|1600)$`)

func checkKey(key string) error {
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("invalid media key %q", key)
	}
	return nil
}

// MediaStoreFromEnv selects the backend:
//
//	KIONGA_BLOG_MEDIA_BACKEND=fs  KIONGA_BLOG_MEDIA_DIR (default data/blog-media)
//	KIONGA_BLOG_MEDIA_BACKEND=s3  KIONGA_BLOG_MEDIA_S3_ENDPOINT, _ACCESS_KEY,
//	                              _SECRET_KEY, _REGION, KIONGA_BLOG_MEDIA_BUCKET
//	                              (default kionga-blog-media)
//
// Unset backend means s3 when an endpoint is configured, else fs.
func MediaStoreFromEnv() MediaStore {
	backend := os.Getenv("KIONGA_BLOG_MEDIA_BACKEND")
	endpoint := os.Getenv("KIONGA_BLOG_MEDIA_S3_ENDPOINT")
	if backend == "" && endpoint != "" {
		backend = "s3"
	}
	if backend == "s3" {
		bucket := os.Getenv("KIONGA_BLOG_MEDIA_BUCKET")
		if bucket == "" {
			bucket = "kionga-blog-media"
		}
		region := os.Getenv("KIONGA_BLOG_MEDIA_S3_REGION")
		if region == "" {
			region = "us-east-1"
		}
		return &S3Store{Bucket: bucket, Client: &http.Client{Timeout: 30 * time.Second}, Config: storage.Config{
			Endpoint: endpoint, Region: region,
			AccessKey: os.Getenv("KIONGA_BLOG_MEDIA_S3_ACCESS_KEY"), SecretKey: os.Getenv("KIONGA_BLOG_MEDIA_S3_SECRET_KEY"),
		}}
	}
	dir := os.Getenv("KIONGA_BLOG_MEDIA_DIR")
	if dir == "" {
		dir = filepath.Join("data", "blog-media")
	}
	return &FSStore{Dir: dir}
}

// FSStore keeps media on the local filesystem (local development, tests).
type FSStore struct{ Dir string }

func (f *FSStore) Name() string { return "fs" }

func (f *FSStore) path(key string) string {
	return filepath.Join(f.Dir, filepath.FromSlash(key))
}

func (f *FSStore) Put(_ context.Context, key, _ string, data []byte) error {
	if err := checkKey(key); err != nil {
		return err
	}
	target := f.path(key)
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

func (f *FSStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	file, err := os.Open(f.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrMediaMissing
	}
	return file, err
}

func (f *FSStore) Delete(_ context.Context, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	err := os.Remove(f.path(key))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	_ = os.Remove(filepath.Dir(f.path(key))) // only succeeds once empty
	return err
}

// S3Store keeps media in an S3-compatible bucket (MinIO locally) using
// SigV4-presigned requests, so no SDK is needed.
type S3Store struct {
	Config storage.Config
	Bucket string
	Client *http.Client
	Now    func() time.Time
}

func (s *S3Store) Name() string { return "s3" }

func (s *S3Store) do(ctx context.Context, method, key, contentType string, body []byte) (*http.Response, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	signed, err := storage.SignURL(s.Config, method, storage.Request{Bucket: s.Bucket, Key: "media/" + key, TTLSeconds: 300}, now)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, signed, reader)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	return s.Client.Do(request)
}

func s3Error(response *http.Response) error {
	detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	return fmt.Errorf("object store returned %d: %s", response.StatusCode, strings.TrimSpace(string(detail)))
}

func (s *S3Store) Put(ctx context.Context, key, contentType string, data []byte) error {
	response, err := s.do(ctx, http.MethodPut, key, contentType, data)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return s3Error(response)
	}
	return nil
}

func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	response, err := s.do(ctx, http.MethodGet, key, "", nil)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusNotFound {
		response.Body.Close()
		return nil, ErrMediaMissing
	}
	if response.StatusCode/100 != 2 {
		defer response.Body.Close()
		return nil, s3Error(response)
	}
	return response.Body, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	response, err := s.do(ctx, http.MethodDelete, key, "", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 && response.StatusCode != http.StatusNotFound {
		return s3Error(response)
	}
	return nil
}
