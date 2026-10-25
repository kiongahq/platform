package storage

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeS3(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("X-Amz-Signature") == "" {
			t.Errorf("request %s not signed", r.URL.Path)
		}
		switch {
		case r.URL.Path == "/":
			_, _ = w.Write([]byte(`<?xml version="1.0"?><ListAllMyBucketsResult><Buckets>
				<Bucket><Name>mlaiops-models</Name><CreationDate>2026-01-01T00:00:00Z</CreationDate></Bucket>
				<Bucket><Name>mlaiops-artifacts</Name><CreationDate>2026-01-02T00:00:00Z</CreationDate></Bucket>
			</Buckets></ListAllMyBucketsResult>`))
		case r.URL.Path == "/mlaiops-models" && r.URL.Query().Get("list-type") == "2":
			if got := r.URL.Query().Get("prefix"); got != "churn/" {
				t.Errorf("prefix not forwarded, got %q", got)
			}
			_, _ = w.Write([]byte(`<?xml version="1.0"?><ListBucketResult>
				<Contents><Key>churn/model.pkl</Key><Size>1024</Size><LastModified>2026-06-01T10:00:00Z</LastModified></Contents>
				<CommonPrefixes><Prefix>churn/v2/</Prefix></CommonPrefixes>
			</ListBucketResult>`))
		case r.URL.Path == "/mlaiops-models/churn/metrics.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"auc": 0.94}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code></Error>`))
		}
	}))
}

func testBrowser(url string) *Browser {
	return NewBrowser(Config{Endpoint: url, AccessKey: "test", SecretKey: "secret"})
}

func TestListBuckets(t *testing.T) {
	server := fakeS3(t)
	defer server.Close()
	buckets, err := testBrowser(server.URL).ListBuckets()
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 || buckets[0].Name != "mlaiops-models" {
		t.Fatalf("unexpected buckets: %v", buckets)
	}
}

func TestListObjectsWithPrefixesAndDelimiter(t *testing.T) {
	server := fakeS3(t)
	defer server.Close()
	listing, err := testBrowser(server.URL).ListObjects("mlaiops-models", "churn/")
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Objects) != 1 || listing.Objects[0].Key != "churn/model.pkl" || listing.Objects[0].Size != 1024 {
		t.Fatalf("unexpected objects: %v", listing.Objects)
	}
	if len(listing.Prefixes) != 1 || listing.Prefixes[0] != "churn/v2/" {
		t.Fatalf("unexpected prefixes: %v", listing.Prefixes)
	}
}

func TestVirtualHostedBrowserUsesBucketRoot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("X-Amz-Signature") == "" {
			t.Error("unsigned request")
		}
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<ListBucketResult><Contents><Key>reports/a.txt</Key></Contents></ListBucketResult>`))
		case "/reports/a.txt":
			_, _ = w.Write([]byte("hello"))
		default:
			t.Errorf("unexpected bucket path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	browser := testBrowser(server.URL)
	browser.BucketInHost = true
	listing, err := browser.ListObjects("data", "reports/")
	if err != nil || len(listing.Objects) != 1 {
		t.Fatalf("virtual-hosted browse: %+v %v", listing, err)
	}
	preview, err := browser.PreviewObject("data", "reports/a.txt")
	if err != nil || preview.Content != "hello" {
		t.Fatalf("virtual-hosted preview: %+v %v", preview, err)
	}
}

func TestPreviewObjectBounded(t *testing.T) {
	server := fakeS3(t)
	defer server.Close()
	browser := testBrowser(server.URL)
	browser.PreviewLimit = 5
	preview, err := browser.PreviewObject("mlaiops-models", "churn/metrics.json")
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Truncated || preview.Content != `{"auc` {
		t.Fatalf("preview not bounded: %+v", preview)
	}
	if preview.ContentType != "application/json" {
		t.Fatalf("content type missing: %+v", preview)
	}
}

func TestBrowseFailsClosedOnUpstreamError(t *testing.T) {
	server := fakeS3(t)
	defer server.Close()
	_, err := testBrowser(server.URL).PreviewObject("mlaiops-models", "missing.txt")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected upstream 404 error, got %v", err)
	}
}

func TestBrowseRequiresConfiguration(t *testing.T) {
	_, err := NewBrowser(Config{}).ListBuckets()
	if err == nil {
		t.Fatal("expected configuration error")
	}
}

func TestPreviewRejectsTraversal(t *testing.T) {
	server := fakeS3(t)
	defer server.Close()
	_, err := testBrowser(server.URL).PreviewObject("bucket", "../../etc/passwd")
	if err == nil {
		t.Fatal("expected traversal rejection")
	}
}
