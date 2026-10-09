package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ml-ai-ops/platform/internal/auth"
	"github.com/ml-ai-ops/platform/internal/feature"
	"github.com/ml-ai-ops/platform/internal/storage"
	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

// Feature stores, feature lineage and external object storage.
//
// Administration lives under /api/v1/admin (admin/operator only). The
// Features page reads /api/v1/features/stores and /api/v1/features/views,
// which return only non-secret summaries scoped to the caller's projects.
// Secrets are referenced as env:VAR, resolved only for a check, and never
// stored or returned.
func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/admin/feature-store-providers", s.featureStoreProviders)
		mux.HandleFunc("GET /api/v1/admin/feature-stores", s.adminFeatureStores)
		mux.HandleFunc("POST /api/v1/admin/feature-stores", s.createFeatureStore)
		mux.HandleFunc("GET /api/v1/admin/feature-stores/{id}", s.adminFeatureStore)
		mux.HandleFunc("PUT /api/v1/admin/feature-stores/{id}", s.updateFeatureStore)
		mux.HandleFunc("DELETE /api/v1/admin/feature-stores/{id}", s.deleteFeatureStore)
		mux.HandleFunc("POST /api/v1/admin/feature-stores/{id}/test", s.testFeatureStore)
		mux.HandleFunc("GET /api/v1/features/stores", s.featureStores)
		mux.HandleFunc("GET /api/v1/features/views", s.featureViewDetails)
		mux.HandleFunc("GET /api/v1/features/{name}/versions", s.featureVersions)
		mux.HandleFunc("GET /api/v1/features/{name}/lineage", s.featureLineage)
		mux.HandleFunc("POST /api/v1/features/{name}/materializations", s.reportFeatureMaterialization)
		mux.HandleFunc("GET /api/v1/admin/storage-connections", s.adminStorageConnections)
		mux.HandleFunc("POST /api/v1/admin/storage-connections", s.createStorageConnection)
		mux.HandleFunc("GET /api/v1/admin/storage-connections/{id}", s.adminStorageConnection)
		mux.HandleFunc("PUT /api/v1/admin/storage-connections/{id}", s.updateStorageConnection)
		mux.HandleFunc("DELETE /api/v1/admin/storage-connections/{id}", s.deleteStorageConnection)
		mux.HandleFunc("POST /api/v1/admin/storage-connections/{id}/test", s.testStorageConnection)
	})
}

const builtinStoreID = "internal"

// featureStoreHealthKind persists the built-in store's last check.
const featureStoreHealthKind = "feature_store_health"

var connectionName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

func resolveSecret(ref string) string {
	if !strings.HasPrefix(ref, "env:") {
		return ""
	}
	return os.Getenv(strings.TrimPrefix(ref, "env:"))
}

func (s *Server) builtinStore() api.FeatureStoreConnection {
	online := os.Getenv("MLAIOPS_FEATURE_GATEWAY_URL")
	offline := os.Getenv("KIONGA_FEATURE_OFFLINE_URI")
	if offline == "" {
		offline = "s3://mlaiops-features"
	}
	connection := api.FeatureStoreConnection{ID: builtinStoreID, Provider: "internal", Name: "kionga-internal", Kind: "internal",
		Config: map[string]string{"online_url": online, "offline_uri": offline}, AllowedProjects: []string{},
		Capabilities: feature.InternalCapabilities, SecretPresent: true}
	connection.Health = api.ConnectionHealth{State: feature.StateConfigured, Detail: "Platform-managed Redis online store and Parquet snapshots. Not checked yet."}
	if online == "" {
		connection.Health.Detail = "Set MLAIOPS_FEATURE_GATEWAY_URL on the gateway so the internal store can be checked."
	}
	if health, err := store.GetDoc[api.ConnectionHealth](s.store, featureStoreHealthKind, builtinStoreID); err == nil {
		connection.Health = health
	}
	return connection
}

func publicConnection(connection api.FeatureStoreConnection) api.FeatureStoreConnection {
	if connection.ID != builtinStoreID {
		connection.SecretPresent = connection.SecretRef != "" && resolveSecret(connection.SecretRef) != ""
	}
	if connection.Config == nil {
		connection.Config = map[string]string{}
	}
	if connection.AllowedProjects == nil {
		connection.AllowedProjects = []string{}
	}
	return connection
}

func (s *Server) featureStoreConnections() []api.FeatureStoreConnection {
	items := []api.FeatureStoreConnection{s.builtinStore()}
	for _, item := range store.ListDocs[api.FeatureStoreConnection](s.store, store.FeatureStoreConnectionKind) {
		items = append(items, publicConnection(item))
	}
	return items
}

func (s *Server) featureStoreProviders(w http.ResponseWriter, _ *http.Request) {
	items := feature.Providers()
	writeJSON(w, http.StatusOK, api.Page[api.FeatureStoreProvider]{Items: items, Total: len(items)})
}

func (s *Server) adminFeatureStores(w http.ResponseWriter, _ *http.Request) {
	items := s.featureStoreConnections()
	writeJSON(w, http.StatusOK, api.Page[api.FeatureStoreConnection]{Items: items, Total: len(items)})
}

func (s *Server) adminFeatureStore(w http.ResponseWriter, r *http.Request) {
	connection, err := s.findFeatureStore(r.PathValue("id"))
	writeMutation(w, connection, err, http.StatusOK)
}

func (s *Server) findFeatureStore(id string) (api.FeatureStoreConnection, error) {
	if id == builtinStoreID {
		return s.builtinStore(), nil
	}
	connection, err := store.GetDoc[api.FeatureStoreConnection](s.store, store.FeatureStoreConnectionKind, id)
	return publicConnection(connection), err
}

// validateProjects checks every referenced project exists.
func (s *Server) validateProjects(ids []string) ([]string, []string) {
	out, issues := []string{}, []string{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || slices.Contains(out, id) {
			continue
		}
		if _, err := s.store.Project(id); err != nil {
			issues = append(issues, "allowed_projects: project "+id+" does not exist")
			continue
		}
		out = append(out, id)
	}
	return out, issues
}

func (s *Server) featureStoreFromRequest(r *http.Request, id string) (api.FeatureStoreConnection, []string, error) {
	var req api.UpsertFeatureStoreConnectionRequest
	if err := decode(r, &req); err != nil {
		return api.FeatureStoreConnection{}, nil, err
	}
	issues := []string{}
	if !connectionName.MatchString(req.Name) {
		issues = append(issues, "name: use 2-63 lowercase letters, digits and hyphens")
	}
	if req.Provider == "internal" {
		issues = append(issues, "provider: the internal store is built in; register external providers here")
	}
	for _, issue := range feature.Validate(req.Provider, req.Config) {
		issues = append(issues, issue.Field+": "+issue.Message)
	}
	if !feature.ValidSecretRef(req.SecretRef) {
		issues = append(issues, "secret_ref: must be empty or env:VARIABLE_NAME")
	}
	projects, projectIssues := s.validateProjects(req.AllowedProjects)
	issues = append(issues, projectIssues...)
	for _, existing := range store.ListDocs[api.FeatureStoreConnection](s.store, store.FeatureStoreConnectionKind) {
		if existing.Name == req.Name && existing.ID != id {
			issues = append(issues, "name: another feature store connection already uses this name")
		}
	}
	provider, _ := feature.LookupProvider(req.Provider)
	return api.FeatureStoreConnection{ID: id, Provider: req.Provider, Name: req.Name, Kind: provider.Kind, Config: req.Config,
		SecretRef: req.SecretRef, AllowedProjects: projects, Capabilities: provider.Capabilities,
		Health: api.ConnectionHealth{State: feature.StateConfigured, Detail: "Saved. Run Test to check it."}}, issues, nil
}

func writeValidation(w http.ResponseWriter, issues []string) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "validation_error", "message": strings.Join(issues, "; "), "details": issues})
}

func (s *Server) createFeatureStore(w http.ResponseWriter, r *http.Request) {
	connection, issues, err := s.featureStoreFromRequest(r, store.NewResourceID("fs"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(issues) > 0 {
		writeValidation(w, issues)
		return
	}
	saved, err := store.SaveFeatureStoreConnection(s.store, connection, true, actor(r))
	writeMutation(w, publicConnection(saved), err, http.StatusCreated)
}

func (s *Server) updateFeatureStore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == builtinStoreID {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "the internal store is configured by the platform environment")
		return
	}
	connection, issues, err := s.featureStoreFromRequest(r, id)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(issues) > 0 {
		writeValidation(w, issues)
		return
	}
	connection.Health = api.ConnectionHealth{State: feature.StateConfigured, Detail: "Configuration changed. Run Test to check it."}
	saved, err := store.SaveFeatureStoreConnection(s.store, connection, false, actor(r))
	writeMutation(w, publicConnection(saved), err, http.StatusOK)
}

func (s *Server) deleteFeatureStore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == builtinStoreID {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "the internal store cannot be deleted")
		return
	}
	if err := s.store.DeleteDocument(store.FeatureStoreConnectionKind, id, "feature_store_connection.deleted", actor(r)); err != nil {
		writeMutation(w, struct{}{}, err, http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkFeatureStore runs the adapter health check with a bounded timeout.
func checkFeatureStore(ctx context.Context, connection api.FeatureStoreConnection) api.ConnectionHealth {
	now := time.Now().UTC()
	health := api.ConnectionHealth{CheckedAt: &now}
	if connection.ID == builtinStoreID && connection.Config["online_url"] == "" {
		health.State, health.Detail = feature.StateConfigured, "Set MLAIOPS_FEATURE_GATEWAY_URL on the gateway so the internal store can be checked."
		return health
	}
	if connection.SecretRef != "" && resolveSecret(connection.SecretRef) == "" {
		health.State, health.Detail = feature.StateConfigured, "The secret reference "+connection.SecretRef+" is not set in the gateway environment."
		return health
	}
	adapter, err := feature.New(connection.Provider, feature.Config{Settings: connection.Config, Secret: resolveSecret(connection.SecretRef)})
	if err != nil {
		health.State, health.Detail = feature.StateUnavailable, err.Error()
		return health
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	result := adapter.Health(ctx)
	health.State, health.Detail = result.State, redactTokens(result.Detail, resolveSecret(connection.SecretRef))
	return health
}

func (s *Server) testFeatureStore(w http.ResponseWriter, r *http.Request) {
	connection, err := s.findFeatureStore(r.PathValue("id"))
	if err != nil {
		writeMutation(w, connection, err, http.StatusOK)
		return
	}
	health := checkFeatureStore(r.Context(), connection)
	if connection.ID == builtinStoreID {
		_, err = store.UpdateDoc(s.store, featureStoreHealthKind, builtinStoreID, func(api.ConnectionHealth, bool) (api.ConnectionHealth, error) {
			return health, nil
		}, "feature_store_connection.tested", actor(r))
		connection.Health = health
		writeMutation(w, connection, err, http.StatusOK)
		return
	}
	saved, err := store.SetFeatureStoreHealth(s.store, connection.ID, health, actor(r))
	writeMutation(w, publicConnection(saved), err, http.StatusOK)
}

func storeLocation(connection api.FeatureStoreConnection) string {
	host := func(raw string) string {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			return ""
		}
		return parsed.Scheme + "://" + parsed.Host
	}
	switch connection.Provider {
	case "internal":
		parts := []string{}
		if online := host(connection.Config["online_url"]); online != "" {
			parts = append(parts, "online "+online)
		} else {
			parts = append(parts, "online Redis (not configured)")
		}
		if offline := connection.Config["offline_uri"]; offline != "" {
			parts = append(parts, "offline "+offline)
		}
		return strings.Join(parts, " · ")
	case "feast":
		return "Feast " + host(connection.Config["url"])
	}
	return connection.Provider
}

// summarizeStore narrows a connection to what a project member may see.
func summarizeStore(connection api.FeatureStoreConnection, allowed map[string]bool) (api.FeatureStoreSummary, bool) {
	summary := api.FeatureStoreSummary{ID: connection.ID, Name: connection.Name, Provider: connection.Provider, Kind: connection.Kind,
		Location: storeLocation(connection), AllowedProjects: []string{}, AllProjects: len(connection.AllowedProjects) == 0,
		Capabilities: connection.Capabilities, Health: connection.Health}
	if summary.AllProjects {
		return summary, true
	}
	for _, id := range connection.AllowedProjects {
		if allowed == nil || allowed[id] {
			summary.AllowedProjects = append(summary.AllowedProjects, id)
		}
	}
	return summary, len(summary.AllowedProjects) > 0
}

func (s *Server) featureStores(w http.ResponseWriter, r *http.Request) {
	allowed := allowedProjectIDs(s.store, principal(r))
	items := []api.FeatureStoreSummary{}
	for _, connection := range s.featureStoreConnections() {
		if summary, ok := summarizeStore(connection, allowed); ok {
			items = append(items, summary)
		}
	}
	writeJSON(w, http.StatusOK, api.Page[api.FeatureStoreSummary]{Items: items, Total: len(items)})
}

func (s *Server) featureViewDetails(w http.ResponseWriter, r *http.Request) {
	builtin, _ := summarizeStore(s.builtinStore(), nil)
	now := time.Now().UTC()
	lineage := store.FeatureMaterializations(s.store, "")
	items := []api.FeatureViewDetail{}
	for _, view := range s.store.FeatureViews() {
		detail := api.FeatureViewDetail{FeatureView: view, Store: builtin}
		if versions := store.FeatureDefinitionVersions(s.store, view.Name); len(versions) > 0 {
			detail.Version = versions[0].Version
		}
		var lastSuccess *time.Time
		for i := range lineage {
			record := lineage[i]
			if record.View != view.Name {
				continue
			}
			if detail.LastRun == nil {
				detail.LastRun = &record
			}
			if record.Status == "failed" && lastSuccess == nil {
				detail.FailureCount++
				if detail.LatestFailure == "" {
					detail.LatestFailure = record.Error
				}
			}
			if record.Status == "succeeded" && lastSuccess == nil {
				created := record.CreatedAt
				lastSuccess = &created
			}
		}
		if lastSuccess == nil {
			// Views materialized before lineage existed only carry a timestamp.
			lastSuccess = view.MaterializedAt
		}
		detail.Freshness = feature.Freshness(lastSuccess, view.TTLSeconds, now)
		items = append(items, detail)
	}
	writeJSON(w, http.StatusOK, api.Page[api.FeatureViewDetail]{Items: items, Total: len(items)})
}

func (s *Server) featureViewExists(name string) (api.FeatureView, bool) {
	for _, view := range s.store.FeatureViews() {
		if view.Name == name {
			return view, true
		}
	}
	return api.FeatureView{}, false
}

func (s *Server) featureVersions(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.featureViewExists(r.PathValue("name")); !ok {
		writeError(w, http.StatusNotFound, "not_found", "feature view not found")
		return
	}
	items := store.FeatureDefinitionVersions(s.store, r.PathValue("name"))
	writeJSON(w, http.StatusOK, api.Page[api.FeatureDefinitionVersion]{Items: items, Total: len(items)})
}

func (s *Server) featureLineage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.featureViewExists(r.PathValue("name")); !ok {
		writeError(w, http.StatusNotFound, "not_found", "feature view not found")
		return
	}
	items := store.FeatureMaterializations(s.store, r.PathValue("name"))
	writeJSON(w, http.StatusOK, api.Page[api.FeatureMaterialization]{Items: items, Total: len(items)})
}

// recordFeatureApply snapshots a definition version after an apply. Called
// from applyFeatureView; failures are logged into the response path by the
// caller, never silently dropped.
func (s *Server) recordFeatureApply(view api.FeatureView, actor string) error {
	_, _, err := store.RecordFeatureDefinitionVersion(s.store, view, builtinStoreID, actor)
	return err
}

var lineageToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@=+-]{0,511}$`)

func (s *Server) reportFeatureMaterialization(w http.ResponseWriter, r *http.Request) {
	caller := principal(r)
	if !privileged(caller) && !slices.Contains(caller.Roles, auth.RoleEngineer) {
		writeError(w, http.StatusForbidden, "access_denied", "only the materializer, engineers and operators report materializations")
		return
	}
	name := r.PathValue("name")
	if _, ok := s.featureViewExists(name); !ok {
		writeError(w, http.StatusNotFound, "not_found", "feature view not found")
		return
	}
	var req api.ReportFeatureMaterializationRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Status == "" {
		req.Status = "succeeded"
	}
	issues := []string{}
	if req.Status != "succeeded" && req.Status != "failed" {
		issues = append(issues, "status: must be succeeded or failed")
	}
	if !lineageToken.MatchString(req.RunID) {
		issues = append(issues, "run_id: required, up to 512 URL-safe characters")
	}
	if !lineageToken.MatchString(req.SourceDataset) {
		issues = append(issues, "source_dataset: required, up to 512 URL-safe characters")
	}
	if req.OfflineURI != "" && !lineageToken.MatchString(req.OfflineURI) {
		issues = append(issues, "offline_uri: up to 512 URL-safe characters")
	}
	if req.EntityCount < 0 {
		issues = append(issues, "entity_count: cannot be negative")
	}
	if len(req.Error) > 2000 {
		req.Error = req.Error[:2000]
	}
	if len(issues) > 0 {
		writeValidation(w, issues)
		return
	}
	if req.ViewVersion == 0 {
		if versions := store.FeatureDefinitionVersions(s.store, name); len(versions) > 0 {
			req.ViewVersion = versions[0].Version
		}
	}
	record, err := store.RecordFeatureMaterialization(s.store, api.FeatureMaterialization{View: name, ViewVersion: req.ViewVersion, RunID: req.RunID,
		SourceDataset: req.SourceDataset, OfflineURI: req.OfflineURI, EntityCount: req.EntityCount, Status: req.Status, Error: req.Error}, actor(r))
	if err == nil && req.Status == "succeeded" {
		_, err = s.store.ReportMaterialization(name, req.EntityCount, actor(r))
	}
	writeMutation(w, record, err, http.StatusCreated)
}

// ---- external object storage -------------------------------------------------

func publicStorage(connection api.StorageConnection) api.StorageConnection {
	connection.SecretPresent = resolveSecret(connection.SecretRef) != ""
	if connection.AllowedProjects == nil {
		connection.AllowedProjects = []string{}
	}
	return connection
}

func (s *Server) adminStorageConnections(w http.ResponseWriter, _ *http.Request) {
	items := []api.StorageConnection{}
	for _, item := range store.ListDocs[api.StorageConnection](s.store, store.StorageConnectionKind) {
		items = append(items, publicStorage(item))
	}
	writeJSON(w, http.StatusOK, api.Page[api.StorageConnection]{Items: items, Total: len(items)})
}

func (s *Server) adminStorageConnection(w http.ResponseWriter, r *http.Request) {
	connection, err := store.GetDoc[api.StorageConnection](s.store, store.StorageConnectionKind, r.PathValue("id"))
	writeMutation(w, publicStorage(connection), err, http.StatusOK)
}

func (s *Server) storageFromRequest(r *http.Request, id string) (api.StorageConnection, []string, error) {
	var req api.UpsertStorageConnectionRequest
	if err := decode(r, &req); err != nil {
		return api.StorageConnection{}, nil, err
	}
	req.Endpoint = strings.TrimRight(strings.TrimSpace(req.Endpoint), "/")
	issues := []string{}
	if !connectionName.MatchString(req.Name) {
		issues = append(issues, "name: use 2-63 lowercase letters, digits and hyphens")
	}
	issues = append(issues, storage.ValidateBucketTarget(storage.BucketTarget{Endpoint: req.Endpoint, Region: req.Region, Bucket: req.Bucket, PathStyle: req.PathStyle, CABundle: req.CABundle})...)
	if req.SecretRef == "" || !feature.ValidSecretRef(req.SecretRef) {
		issues = append(issues, "secret_ref: required as env:VARIABLE_NAME holding ACCESS_KEY_ID:SECRET_ACCESS_KEY")
	}
	if strings.Contains(req.CABundle, "PRIVATE KEY") {
		issues = append(issues, "ca_bundle: must contain only public CA certificates")
	}
	projects, projectIssues := s.validateProjects(req.AllowedProjects)
	issues = append(issues, projectIssues...)
	for _, existing := range store.ListDocs[api.StorageConnection](s.store, store.StorageConnectionKind) {
		if existing.Name == req.Name && existing.ID != id {
			issues = append(issues, "name: another storage connection already uses this name")
		}
	}
	return api.StorageConnection{ID: id, Name: req.Name, Endpoint: req.Endpoint, Region: req.Region, Bucket: req.Bucket, PathStyle: req.PathStyle,
		CABundle: req.CABundle, SecretRef: req.SecretRef, AllowedProjects: projects,
		Health: api.ConnectionHealth{State: feature.StateConfigured, Detail: "Saved. Run Test to check it."}}, issues, nil
}

func (s *Server) createStorageConnection(w http.ResponseWriter, r *http.Request) {
	connection, issues, err := s.storageFromRequest(r, store.NewResourceID("st"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(issues) > 0 {
		writeValidation(w, issues)
		return
	}
	saved, err := store.SaveStorageConnection(s.store, connection, true, actor(r))
	writeMutation(w, publicStorage(saved), err, http.StatusCreated)
}

func (s *Server) updateStorageConnection(w http.ResponseWriter, r *http.Request) {
	connection, issues, err := s.storageFromRequest(r, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(issues) > 0 {
		writeValidation(w, issues)
		return
	}
	connection.Health = api.ConnectionHealth{State: feature.StateConfigured, Detail: "Configuration changed. Run Test to check it."}
	saved, err := store.SaveStorageConnection(s.store, connection, false, actor(r))
	writeMutation(w, publicStorage(saved), err, http.StatusOK)
}

func (s *Server) deleteStorageConnection(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteDocument(store.StorageConnectionKind, r.PathValue("id"), "storage_connection.deleted", actor(r)); err != nil {
		writeMutation(w, struct{}{}, err, http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// storageCheckClient lets tests inject a transport; nil uses the target's
// own client (system roots plus ca_bundle).
var storageCheckClient func(storage.BucketTarget) *http.Client

func (s *Server) testStorageConnection(w http.ResponseWriter, r *http.Request) {
	connection, err := store.GetDoc[api.StorageConnection](s.store, store.StorageConnectionKind, r.PathValue("id"))
	if err != nil {
		writeMutation(w, connection, err, http.StatusOK)
		return
	}
	secret := resolveSecret(connection.SecretRef)
	accessKey, secretKey, _ := strings.Cut(secret, ":")
	target := storage.BucketTarget{Endpoint: connection.Endpoint, Region: connection.Region, Bucket: connection.Bucket, PathStyle: connection.PathStyle,
		CABundle: connection.CABundle, AccessKey: accessKey, SecretKey: secretKey}
	now := time.Now().UTC()
	health := api.ConnectionHealth{CheckedAt: &now}
	var client *http.Client
	if storageCheckClient != nil {
		client = storageCheckClient(target)
	} else {
		client, err = target.HTTPClient(8 * time.Second)
	}
	if err != nil {
		health.State, health.Detail = feature.StateUnavailable, err.Error()
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		health.State, health.Detail = storage.CheckBucket(ctx, target, client)
		cancel()
		health.Detail = redactTokens(health.Detail, accessKey, secretKey)
	}
	saved, err := store.SetStorageHealth(s.store, connection.ID, health, actor(r))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "storage connection not found")
		return
	}
	writeMutation(w, publicStorage(saved), err, http.StatusOK)
}
