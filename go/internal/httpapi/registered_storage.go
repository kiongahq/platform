package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/storage"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// Registered storage uses the administrator's existing project grants and
// secret reference. The gateway signs requests and only returns bounded data.
type registeredStorageSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Bucket string `json:"bucket"`
}

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/registered-storage", s.registeredStorage)
		mux.HandleFunc("GET /api/v1/registered-storage/{id}/objects", s.registeredStorageObjects)
		mux.HandleFunc("GET /api/v1/registered-storage/{id}/object", s.registeredStorageObject)
	})
}

func (s *Server) storageConnectionAllowed(r *http.Request, item api.StorageConnection) bool {
	for _, projectID := range item.AllowedProjects {
		if projectAllowed(s.store, principal(r), projectID) {
			return true
		}
	}
	return false
}

func (s *Server) registeredStorage(w http.ResponseWriter, r *http.Request) {
	items := []registeredStorageSummary{}
	for _, item := range store.ListDocs[api.StorageConnection](s.store, store.StorageConnectionKind) {
		if s.storageConnectionAllowed(r, item) {
			items = append(items, registeredStorageSummary{ID: item.ID, Name: item.Name, Bucket: item.Bucket})
		}
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) authorizedStorageBrowser(w http.ResponseWriter, r *http.Request) (*storage.Browser, string, bool) {
	item, err := store.GetDoc[api.StorageConnection](s.store, store.StorageConnectionKind, r.PathValue("id"))
	if err != nil || !s.storageConnectionAllowed(r, item) {
		writeError(w, 404, "not_found", "Storage connection not found")
		return nil, "", false
	}
	secret := resolveSecret(item.SecretRef)
	accessKey, secretKey, valid := strings.Cut(secret, ":")
	if !valid || accessKey == "" || secretKey == "" {
		writeError(w, 503, "connection_unavailable", "Storage connection credentials are unavailable")
		return nil, "", false
	}
	client, err := (storage.BucketTarget{CABundle: item.CABundle}).HTTPClient(15 * time.Second)
	if err != nil {
		writeError(w, 503, "connection_unavailable", "Storage connection TLS configuration is invalid")
		return nil, "", false
	}
	endpoint := item.Endpoint
	if !item.PathStyle {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" {
			writeError(w, 503, "connection_unavailable", "Storage connection endpoint is invalid")
			return nil, "", false
		}
		parsed.Host = item.Bucket + "." + parsed.Host
		endpoint = parsed.String()
	}
	browser := storage.NewBrowser(storage.Config{Endpoint: endpoint, Region: item.Region, AccessKey: accessKey, SecretKey: secretKey})
	browser.Client = client
	browser.BucketInHost = !item.PathStyle
	return browser, item.Bucket, true
}

func (s *Server) registeredStorageObjects(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	if len(prefix) > 512 || strings.Contains(prefix, "..") || strings.HasPrefix(prefix, "/") {
		writeError(w, 422, "invalid_request", "Use a relative object prefix")
		return
	}
	browser, bucket, ok := s.authorizedStorageBrowser(w, r)
	if !ok {
		return
	}
	listing, err := browser.ListObjects(bucket, prefix)
	if err != nil {
		writeError(w, 502, "storage_unavailable", "Could not browse registered storage")
		return
	}
	writeJSON(w, 200, listing)
}

func (s *Server) registeredStorageObject(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" || len(key) > 1024 || strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		writeError(w, 422, "invalid_request", "Use a valid object key")
		return
	}
	browser, bucket, ok := s.authorizedStorageBrowser(w, r)
	if !ok {
		return
	}
	preview, err := browser.PreviewObject(bucket, key)
	if err != nil {
		writeError(w, 502, "storage_unavailable", "Could not preview registered storage object")
		return
	}
	writeJSON(w, 200, preview)
}
