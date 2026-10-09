package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string
}

type Request struct {
	Bucket     string `json:"bucket"`
	Key        string `json:"key"`
	Operation  string `json:"operation"`
	TTLSeconds int    `json:"ttl_seconds"`
}

func Presign(config Config, request Request, now time.Time) (string, error) {
	if config.Endpoint == "" || config.AccessKey == "" || config.SecretKey == "" {
		return "", errors.New("storage endpoint and credentials must be configured")
	}
	if request.Bucket == "" || request.Key == "" || strings.Contains(request.Bucket, "/") || strings.Contains(request.Key, "..") {
		return "", errors.New("valid bucket and key are required")
	}
	method := strings.ToUpper(request.Operation)
	if method != "GET" && method != "PUT" {
		return "", errors.New("operation must be GET or PUT")
	}
	return SignURL(config, method, request, now)
}

// SignURL presigns any S3 method for trusted in-process callers (the blog
// media store needs DELETE). The storage proxy goes through Presign, which
// keeps client-requested operations limited to GET and PUT.
func SignURL(config Config, method string, request Request, now time.Time) (string, error) {
	if config.Endpoint == "" || config.AccessKey == "" || config.SecretKey == "" {
		return "", errors.New("storage endpoint and credentials must be configured")
	}
	if request.Bucket == "" || request.Key == "" || strings.Contains(request.Bucket, "/") || strings.Contains(request.Key, "..") {
		return "", errors.New("valid bucket and key are required")
	}
	if request.TTLSeconds <= 0 {
		request.TTLSeconds = 300
	}
	if request.TTLSeconds > 900 {
		return "", errors.New("ttl_seconds cannot exceed 900")
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil {
		return "", err
	}
	if config.Region == "" {
		config.Region = "us-east-1"
	}
	now = now.UTC()
	date, timestamp := now.Format("20060102"), now.Format("20060102T150405Z")
	scope := fmt.Sprintf("%s/%s/s3/aws4_request", date, config.Region)
	// path.Join drops the leading slash when the base path is empty; the
	// canonical request must sign the absolute path or S3 rejects the
	// signature.
	endpoint.Path = path.Join("/", endpoint.Path, request.Bucket, request.Key)
	query := endpoint.Query()
	query.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	query.Set("X-Amz-Credential", config.AccessKey+"/"+scope)
	query.Set("X-Amz-Date", timestamp)
	query.Set("X-Amz-Expires", strconv.Itoa(request.TTLSeconds))
	query.Set("X-Amz-SignedHeaders", "host")
	canonicalQuery := encodeQuery(query)
	canonical := strings.Join([]string{method, endpoint.EscapedPath(), canonicalQuery, "host:" + endpoint.Host + "\n", "host", "UNSIGNED-PAYLOAD"}, "\n")
	hash := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + timestamp + "\n" + scope + "\n" + hex.EncodeToString(hash[:])
	key := hmacSHA([]byte("AWS4"+config.SecretKey), date)
	key = hmacSHA(key, config.Region)
	key = hmacSHA(key, "s3")
	key = hmacSHA(key, "aws4_request")
	query.Set("X-Amz-Signature", hex.EncodeToString(hmacSHA(key, toSign)))
	endpoint.RawQuery = encodeQuery(query)
	return endpoint.String(), nil
}

func hmacSHA(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(value))
	return h.Sum(nil)
}
func encodeQuery(values url.Values) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := values[k]
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, url.QueryEscape(k)+"="+strings.ReplaceAll(url.QueryEscape(v), "+", "%20"))
		}
	}
	return strings.Join(parts, "&")
}
