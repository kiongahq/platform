package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func hexString(value []byte) string { return hex.EncodeToString(value) }

// Browser provides read access to the S3-compatible object store for the
// console's Storage Explorer: bucket listing, prefix listing, and bounded
// object previews. It signs every request itself (SigV4 query signing via
// Presign), so callers never see credentials.
type Browser struct {
	Config Config
	Client *http.Client
	// BucketInHost uses virtual-hosted S3 URLs for an external bucket.
	BucketInHost bool
	// PreviewLimit caps object preview bytes; defaults to 64 KiB.
	PreviewLimit int64
}

func NewBrowser(config Config) *Browser {
	return &Browser{Config: config, Client: &http.Client{Timeout: 15 * time.Second}, PreviewLimit: 64 << 10}
}

type Bucket struct {
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

type Object struct {
	Key          string `json:"key"`
	Size         int64  `json:"size"`
	LastModified string `json:"last_modified"`
}

type Listing struct {
	Bucket   string   `json:"bucket"`
	Prefix   string   `json:"prefix"`
	Objects  []Object `json:"objects"`
	Prefixes []string `json:"prefixes"`
}

type Preview struct {
	Bucket      string `json:"bucket"`
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Truncated   bool   `json:"truncated"`
	Content     string `json:"content"`
}

// signedURL builds a presigned URL for an arbitrary path + query. Presign
// covers bucket/key operations; bucket-level listings sign the raw path here
// with the same key-derivation helpers.
func (b *Browser) signedURL(method, rawPath string, query url.Values, now time.Time) (string, error) {
	if b.Config.Endpoint == "" || b.Config.AccessKey == "" || b.Config.SecretKey == "" {
		return "", errors.New("storage endpoint and credentials must be configured")
	}
	endpoint, err := url.Parse(b.Config.Endpoint)
	if err != nil {
		return "", err
	}
	region := b.Config.Region
	if region == "" {
		region = "us-east-1"
	}
	now = now.UTC()
	date, timestamp := now.Format("20060102"), now.Format("20060102T150405Z")
	scope := fmt.Sprintf("%s/%s/s3/aws4_request", date, region)
	endpoint.Path = rawPath
	signable := url.Values{}
	for name, values := range query {
		signable[name] = values
	}
	signable.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	signable.Set("X-Amz-Credential", b.Config.AccessKey+"/"+scope)
	signable.Set("X-Amz-Date", timestamp)
	signable.Set("X-Amz-Expires", "300")
	signable.Set("X-Amz-SignedHeaders", "host")
	canonical := strings.Join([]string{method, endpoint.EscapedPath(), encodeQuery(signable), "host:" + endpoint.Host + "\n", "host", "UNSIGNED-PAYLOAD"}, "\n")
	hash := sha256Hex(canonical)
	toSign := "AWS4-HMAC-SHA256\n" + timestamp + "\n" + scope + "\n" + hash
	key := hmacSHA([]byte("AWS4"+b.Config.SecretKey), date)
	key = hmacSHA(key, region)
	key = hmacSHA(key, "s3")
	key = hmacSHA(key, "aws4_request")
	signable.Set("X-Amz-Signature", hexString(hmacSHA(key, toSign)))
	endpoint.RawQuery = encodeQuery(signable)
	return endpoint.String(), nil
}

func (b *Browser) get(rawPath string, query url.Values) (*http.Response, error) {
	signed, err := b.signedURL(http.MethodGet, rawPath, query, time.Now())
	if err != nil {
		return nil, err
	}
	response, err := b.Client.Get(signed)
	if err != nil {
		return nil, fmt.Errorf("object store unreachable: %w", err)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		defer func() { _ = response.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return nil, fmt.Errorf("object store returned %s: %s", response.Status, strings.TrimSpace(string(raw)))
	}
	return response, nil
}

type listBucketsXML struct {
	Buckets struct {
		Bucket []struct {
			Name         string `xml:"Name"`
			CreationDate string `xml:"CreationDate"`
		} `xml:"Bucket"`
	} `xml:"Buckets"`
}

func (b *Browser) ListBuckets() ([]Bucket, error) {
	response, err := b.get("/", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	var decoded listBucketsXML
	if err := xml.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("invalid bucket listing: %w", err)
	}
	buckets := make([]Bucket, 0, len(decoded.Buckets.Bucket))
	for _, bucket := range decoded.Buckets.Bucket {
		buckets = append(buckets, Bucket{Name: bucket.Name, CreatedAt: bucket.CreationDate})
	}
	return buckets, nil
}

type listObjectsXML struct {
	Contents []struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
}

func (b *Browser) ListObjects(bucket, prefix string) (Listing, error) {
	if bucket == "" || strings.Contains(bucket, "/") {
		return Listing{}, errors.New("valid bucket is required")
	}
	query := url.Values{}
	query.Set("list-type", "2")
	query.Set("delimiter", "/")
	query.Set("max-keys", "500")
	if prefix != "" {
		query.Set("prefix", prefix)
	}
	path := "/" + bucket
	if b.BucketInHost {
		path = "/"
	}
	response, err := b.get(path, query)
	if err != nil {
		return Listing{}, err
	}
	defer func() { _ = response.Body.Close() }()
	var decoded listObjectsXML
	if err := xml.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return Listing{}, fmt.Errorf("invalid object listing: %w", err)
	}
	listing := Listing{Bucket: bucket, Prefix: prefix, Objects: []Object{}, Prefixes: []string{}}
	for _, object := range decoded.Contents {
		listing.Objects = append(listing.Objects, Object{Key: object.Key, Size: object.Size, LastModified: object.LastModified})
	}
	for _, common := range decoded.CommonPrefixes {
		listing.Prefixes = append(listing.Prefixes, common.Prefix)
	}
	return listing, nil
}

func (b *Browser) PreviewObject(bucket, key string) (Preview, error) {
	if bucket == "" || key == "" || strings.Contains(key, "..") {
		return Preview{}, errors.New("valid bucket and key are required")
	}
	limit := b.PreviewLimit
	if limit <= 0 {
		limit = 64 << 10
	}
	path := "/" + bucket + "/" + key
	if b.BucketInHost {
		path = "/" + key
	}
	response, err := b.get(path, nil)
	if err != nil {
		return Preview{}, err
	}
	defer func() { _ = response.Body.Close() }()
	content, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return Preview{}, err
	}
	preview := Preview{
		Bucket:      bucket,
		Key:         key,
		ContentType: response.Header.Get("Content-Type"),
		Size:        response.ContentLength,
	}
	if int64(len(content)) > limit {
		preview.Truncated = true
		content = content[:limit]
	}
	preview.Content = string(content)
	return preview, nil
}
