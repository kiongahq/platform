package httpapi

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/store"
)

// A dataset is a project-scoped reference to a prefix in an already governed
// storage bucket. Object credentials and bytes remain with the storage proxy.
const datasetKind = "project_dataset"

var datasetBucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

type projectDataset struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Bucket      string    `json:"bucket"`
	Prefix      string    `json:"prefix"`
	CreatedAt   time.Time `json:"created_at"`
}

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/admin/datasets", s.createProjectDataset)
		mux.HandleFunc("GET /api/v1/datasets", s.projectDatasets)
		mux.HandleFunc("GET /api/v1/datasets/{id}/objects", s.datasetObjects)
		mux.HandleFunc("GET /api/v1/datasets/{id}/object", s.datasetObject)
	})
}

func (s *Server) createProjectDataset(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var dataset projectDataset
	if err := decode(r, &dataset); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	if dataset.ProjectID == "" || !projectAllowed(s.store, principal(r), dataset.ProjectID) {
		writeError(w, 403, "forbidden", "Project is not assigned")
		return
	}
	if _, err := s.store.Project(dataset.ProjectID); err != nil {
		writeError(w, 404, "not_found", "Project not found")
		return
	}
	if !connectionName.MatchString(dataset.Name) || len(dataset.Description) > 500 ||
		!datasetBucketName.MatchString(dataset.Bucket) || strings.Contains(dataset.Bucket, "..") ||
		len(dataset.Prefix) > 512 || strings.HasPrefix(dataset.Prefix, "/") ||
		strings.Contains(dataset.Prefix, "..") {
		writeError(w, 422, "invalid_request", "Use a lowercase name, bucket and a relative prefix without parent segments")
		return
	}
	// A dataset names a directory-like prefix. Without a trailing slash, a
	// grant for "training/churn" would also include "training/churn-private".
	if dataset.Prefix != "" && !strings.HasSuffix(dataset.Prefix, "/") {
		dataset.Prefix += "/"
	}
	for _, existing := range store.ListDocs[projectDataset](s.store, datasetKind) {
		if existing.ProjectID == dataset.ProjectID && existing.Name == dataset.Name {
			writeError(w, 409, "already_exists", "Dataset name already exists in this project")
			return
		}
	}
	dataset.ID = "ds-" + knowledgeID()
	dataset.CreatedAt = time.Now().UTC()
	saved, err := store.UpdateDoc(s.store, datasetKind, dataset.ID, func(_ projectDataset, exists bool) (projectDataset, error) {
		if exists {
			return projectDataset{}, store.ErrConflict
		}
		return dataset, nil
	}, "dataset.created", actor(r))
	writeMutation(w, saved, err, http.StatusCreated)
}

func (s *Server) projectDatasets(w http.ResponseWriter, r *http.Request) {
	items := []projectDataset{}
	for _, dataset := range store.ListDocs[projectDataset](s.store, datasetKind) {
		if projectAllowed(s.store, principal(r), dataset.ProjectID) && storageAllowed(s.store, principal(r), dataset.Bucket) {
			items = append(items, dataset)
		}
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) authorizedDataset(w http.ResponseWriter, r *http.Request) (projectDataset, bool) {
	dataset, err := store.GetDoc[projectDataset](s.store, datasetKind, r.PathValue("id"))
	if err != nil || !projectAllowed(s.store, principal(r), dataset.ProjectID) || !storageAllowed(s.store, principal(r), dataset.Bucket) {
		writeError(w, 404, "not_found", "Dataset not found")
		return projectDataset{}, false
	}
	return dataset, true
}

func (s *Server) datasetObjects(w http.ResponseWriter, r *http.Request) {
	dataset, ok := s.authorizedDataset(w, r)
	if !ok {
		return
	}
	suffix := r.URL.Query().Get("prefix")
	if len(suffix) > 512 || strings.Contains(suffix, "..") || strings.HasPrefix(suffix, "/") {
		writeError(w, 422, "invalid_request", "Use a relative object prefix")
		return
	}
	query := url.Values{"bucket": {dataset.Bucket}, "prefix": {dataset.Prefix + suffix}}
	r.URL.RawQuery = query.Encode()
	s.storageProxy("/objects")(w, r)
}

func (s *Server) datasetObject(w http.ResponseWriter, r *http.Request) {
	dataset, ok := s.authorizedDataset(w, r)
	if !ok {
		return
	}
	key := r.URL.Query().Get("key")
	if len(key) == 0 || len(key) > 1024 || !strings.HasPrefix(key, dataset.Prefix) {
		writeError(w, 422, "invalid_request", "Object key is outside this dataset")
		return
	}
	query := url.Values{"bucket": {dataset.Bucket}, "key": {key}}
	r.URL.RawQuery = query.Encode()
	s.storageProxy("/object")(w, r)
}
