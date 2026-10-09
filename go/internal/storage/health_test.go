package storage

import (
	"context"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testSecret = "s3cr3t-key"

// fakeBucketServer verifies the SigV4 signature like S3 and answers per bucket.
func fakeBucketServer(t *testing.T, tls bool) *httptest.Server {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		verifySigV4(t, http.MethodHead, scheme+"://"+r.Host+r.URL.RequestURI(), testSecret)
		bucket := strings.Trim(r.URL.Path, "/")
		if bucket == "" {
			bucket, _, _ = strings.Cut(r.Host, ".")
		}
		switch bucket {
		case "lake":
			w.WriteHeader(http.StatusOK)
		case "elsewhere":
			w.Header().Set("X-Amz-Bucket-Region", "eu-central-1")
			w.WriteHeader(http.StatusMovedPermanently)
		case "locked":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	if tls {
		return httptest.NewTLSServer(handler)
	}
	return httptest.NewServer(handler)
}

func target(endpoint, bucket string) BucketTarget {
	return BucketTarget{Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, PathStyle: true, AccessKey: "AKIDEXAMPLE", SecretKey: testSecret}
}

func TestCheckBucketClassifiesResponses(t *testing.T) {
	server := fakeBucketServer(t, false)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cases := map[string]string{"lake": "healthy", "elsewhere": "degraded", "locked": "unavailable", "missing": "unavailable"}
	for bucket, want := range cases {
		state, detail := CheckBucket(context.Background(), target(server.URL, bucket), client)
		if state != want {
			t.Fatalf("%s: got %s (%s) want %s", bucket, state, detail, want)
		}
		if strings.Contains(detail, testSecret) || strings.Contains(detail, "AKIDEXAMPLE") {
			t.Fatalf("detail leaked credentials: %s", detail)
		}
	}
	if _, detail := CheckBucket(context.Background(), target(server.URL, "elsewhere"), client); !strings.Contains(detail, "eu-central-1") {
		t.Fatalf("region mismatch must name the region: %s", detail)
	}
}

func TestCheckBucketVirtualHostedStyle(t *testing.T) {
	server := fakeBucketServer(t, false)
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "http://")
	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	value := target("http://s3.test.local:"+strings.Split(address, ":")[1], "lake")
	value.PathStyle = false
	if state, detail := CheckBucket(context.Background(), value, client); state != "healthy" {
		t.Fatalf("virtual-hosted: %s %s", state, detail)
	}
}

func TestCheckBucketTrustsOnlyTheConfiguredCA(t *testing.T) {
	server := fakeBucketServer(t, true)
	defer server.Close()
	untrusted, _ := target(server.URL, "lake").HTTPClient(5 * time.Second)
	if state, detail := CheckBucket(context.Background(), target(server.URL, "lake"), untrusted); state != "unavailable" || !strings.Contains(detail, "ca_bundle") {
		t.Fatalf("unknown CA must fail with guidance: %s %s", state, detail)
	}
	withCA := target(server.URL, "lake")
	withCA.CABundle = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	trusted, err := withCA.HTTPClient(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if state, detail := CheckBucket(context.Background(), withCA, trusted); state != "healthy" {
		t.Fatalf("configured CA must be trusted: %s %s", state, detail)
	}
}

func TestCheckBucketWithoutCredentialsIsOnlyConfigured(t *testing.T) {
	value := target("https://s3.example.com", "lake")
	value.AccessKey, value.SecretKey = "", ""
	if state, _ := CheckBucket(context.Background(), value, http.DefaultClient); state != "configured" {
		t.Fatalf("missing credentials: %s", state)
	}
	unreachable := target("http://127.0.0.1:1", "lake")
	if state, detail := CheckBucket(context.Background(), unreachable, &http.Client{Timeout: time.Second}); state != "unavailable" || !strings.Contains(detail, "unreachable") {
		t.Fatalf("unreachable: %s %s", state, detail)
	}
}

func TestValidateBucketTarget(t *testing.T) {
	bad := []BucketTarget{
		{Endpoint: "", Bucket: "lake"},
		{Endpoint: "ftp://s3", Bucket: "lake"},
		{Endpoint: "https://key:secret@s3.example.com", Bucket: "lake"},
		{Endpoint: "https://s3.example.com/path", Bucket: "lake"},
		{Endpoint: "https://s3.example.com", Bucket: "../etc"},
		{Endpoint: "https://s3.example.com", Bucket: "UPPER"},
		{Endpoint: "https://s3.example.com", Bucket: "lake", Region: "us east"},
		{Endpoint: "https://s3.example.com", Bucket: "my.lake"},
		{Endpoint: "https://s3.example.com", Bucket: "lake", CABundle: "not a cert"},
	}
	for _, value := range bad {
		if len(ValidateBucketTarget(value)) == 0 {
			t.Fatalf("expected rejection for %+v", value)
		}
	}
	if issues := ValidateBucketTarget(BucketTarget{Endpoint: "https://s3.eu-west-1.amazonaws.com", Region: "eu-west-1", Bucket: "my.lake", PathStyle: true}); len(issues) != 0 {
		t.Fatalf("valid target rejected: %v", issues)
	}
}
