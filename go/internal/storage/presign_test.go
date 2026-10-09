package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
	"time"
)

// verifySigV4 recomputes the signature exactly the way an S3 server does —
// from the URL's actual escaped path and received query — and compares. This
// is the check that catches "URL looks right, signature signed a different
// path" bugs.
func verifySigV4(t *testing.T, method, rawURL, secret string) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	provided := query.Get("X-Amz-Signature")
	query.Del("X-Amz-Signature")
	credential := strings.Split(query.Get("X-Amz-Credential"), "/")
	if len(credential) != 5 {
		t.Fatalf("malformed credential: %v", credential)
	}
	date, region := credential[1], credential[2]
	scope := strings.Join(credential[1:], "/")
	canonical := strings.Join([]string{method, parsed.EscapedPath(), encodeQuery(query), "host:" + parsed.Host + "\n", "host", "UNSIGNED-PAYLOAD"}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + query.Get("X-Amz-Date") + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	key := hmacSHA([]byte("AWS4"+secret), date)
	key = hmacSHA(key, region)
	key = hmacSHA(key, "s3")
	key = hmacSHA(key, "aws4_request")
	expected := hex.EncodeToString(hmacSHA(key, toSign))
	if !hmac.Equal([]byte(expected), []byte(provided)) {
		t.Fatalf("server-side verification failed:\ncanonical=%q\nexpected=%s provided=%s", canonical, expected, provided)
	}
}

func TestPresignVerifiesLikeAnS3Server(t *testing.T) {
	signed, err := Presign(
		Config{Endpoint: "http://objectstore:9000", AccessKey: "mlaiops", SecretKey: "secret"},
		Request{Bucket: "mlaiops-features", Key: "customer_profile/snapshot.parquet", Operation: "PUT", TTLSeconds: 300},
		time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(signed)
	if !strings.HasPrefix(parsed.EscapedPath(), "/mlaiops-features/") {
		t.Fatalf("signed path must be absolute: %q", parsed.EscapedPath())
	}
	verifySigV4(t, "PUT", signed, "secret")
}

func TestBrowserURLsVerifyLikeAnS3Server(t *testing.T) {
	browser := NewBrowser(Config{Endpoint: "http://objectstore:9000", AccessKey: "mlaiops", SecretKey: "secret"})
	query := url.Values{}
	query.Set("list-type", "2")
	signed, err := browser.signedURL("GET", "/mlaiops-models", query, time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	verifySigV4(t, "GET", signed, "secret")
}

func TestPresignCreatesBoundedSigV4URL(t *testing.T) {
	got, err := Presign(Config{Endpoint: "http://objectstore:9000", Region: "us-east-1", AccessKey: "key", SecretKey: "secret"}, Request{Bucket: "models", Key: "churn/1/model.pkl", Operation: "GET", TTLSeconds: 300}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"models/churn/1/model.pkl", "X-Amz-Algorithm=AWS4-HMAC-SHA256", "X-Amz-Signature="} {
		if !strings.Contains(got, expected) {
			t.Fatalf("URL missing %q: %s", expected, got)
		}
	}
}

func TestPresignRejectsExcessiveTTL(t *testing.T) {
	_, err := Presign(Config{Endpoint: "http://objectstore", AccessKey: "key", SecretKey: "secret"}, Request{Bucket: "x", Key: "x", Operation: "PUT", TTLSeconds: 901}, time.Now())
	if err == nil {
		t.Fatal("expected TTL error")
	}
}
