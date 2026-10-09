package api

import "time"

// ConnectionHealth is the last active check of an external system. State is
// one of configured (saved, never checked), healthy, degraded or unavailable.
type ConnectionHealth struct {
	State     string     `json:"state"`
	Detail    string     `json:"detail"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

// FeatureStoreCapabilities reports what Kionga can drive through an adapter,
// not everything the upstream product can do.
type FeatureStoreCapabilities struct {
	Online      bool `json:"online"`
	Offline     bool `json:"offline"`
	PointInTime bool `json:"point_in_time"`
	Materialize bool `json:"materialize"`
	TTL         bool `json:"ttl"`
	Push        bool `json:"push"`
}

// FeatureStoreProvider is one entry of the adapter registry. Providers
// without an adapter are listed so operators can see the contract exists.
type FeatureStoreProvider struct {
	Name         string                   `json:"name"`
	Title        string                   `json:"title"`
	Kind         string                   `json:"kind"` // internal | external
	Adapter      bool                     `json:"adapter"`
	Status       string                   `json:"status"`
	Capabilities FeatureStoreCapabilities `json:"capabilities"`
	ConfigKeys   []string                 `json:"config_keys"`
}

// FeatureStoreConnection binds an adapter to non-secret configuration. The
// secret is referenced (env:VAR) and resolved only at check/lookup time; it
// is never stored or returned.
type FeatureStoreConnection struct {
	ID              string                   `json:"id"`
	Provider        string                   `json:"provider"`
	Name            string                   `json:"name"`
	Kind            string                   `json:"kind"`
	Config          map[string]string        `json:"config"`
	SecretRef       string                   `json:"secret_ref,omitempty"`
	SecretPresent   bool                     `json:"secret_present"`
	AllowedProjects []string                 `json:"allowed_projects"`
	Capabilities    FeatureStoreCapabilities `json:"capabilities"`
	Health          ConnectionHealth         `json:"health"`
	CreatedBy       string                   `json:"created_by"`
	CreatedAt       time.Time                `json:"created_at"`
	UpdatedAt       time.Time                `json:"updated_at"`
}

type UpsertFeatureStoreConnectionRequest struct {
	Provider        string            `json:"provider"`
	Name            string            `json:"name"`
	Config          map[string]string `json:"config"`
	SecretRef       string            `json:"secret_ref,omitempty"`
	AllowedProjects []string          `json:"allowed_projects"`
}

// FeatureStoreSummary is the non-administrative view of a store shown on the
// Features page: where it lives, whether it works, and who can use it.
type FeatureStoreSummary struct {
	ID              string                   `json:"id"`
	Name            string                   `json:"name"`
	Provider        string                   `json:"provider"`
	Kind            string                   `json:"kind"`
	Location        string                   `json:"location"`
	AllowedProjects []string                 `json:"allowed_projects"`
	AllProjects     bool                     `json:"all_projects"`
	Capabilities    FeatureStoreCapabilities `json:"capabilities"`
	Health          ConnectionHealth         `json:"health"`
}

// FeatureDefinitionVersion is an immutable snapshot of a feature view
// definition. A new version is written only when the definition changes.
type FeatureDefinitionVersion struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Version    int            `json:"version"`
	SHA256     string         `json:"sha256"`
	Entity     string         `json:"entity"`
	Fields     []FeatureField `json:"fields"`
	Tags       []string       `json:"tags"`
	Source     string         `json:"source"`
	TTLSeconds int            `json:"ttl_seconds"`
	StoreID    string         `json:"store_id"`
	CreatedBy  string         `json:"created_by"`
	CreatedAt  time.Time      `json:"created_at"`
}

// FeatureMaterialization is the lineage record of one materialization run:
// which source dataset produced which view version, where the offline
// snapshot was written and how many entities reached the online store.
type FeatureMaterialization struct {
	ID            string    `json:"id"`
	View          string    `json:"view"`
	ViewVersion   int       `json:"view_version"`
	RunID         string    `json:"run_id"`
	SourceDataset string    `json:"source_dataset"`
	OfflineURI    string    `json:"offline_uri,omitempty"`
	EntityCount   int       `json:"entity_count"`
	Status        string    `json:"status"` // succeeded | failed
	Error         string    `json:"error,omitempty"`
	ReportedBy    string    `json:"reported_by"`
	CreatedAt     time.Time `json:"created_at"`
}

type ReportFeatureMaterializationRequest struct {
	RunID         string `json:"run_id"`
	SourceDataset string `json:"source_dataset"`
	OfflineURI    string `json:"offline_uri,omitempty"`
	EntityCount   int    `json:"entity_count"`
	Status        string `json:"status,omitempty"`
	Error         string `json:"error,omitempty"`
	ViewVersion   int    `json:"view_version,omitempty"`
}

// FeatureFreshness compares the last successful materialization with the
// view TTL: fresh, stale or never.
type FeatureFreshness struct {
	State      string `json:"state"`
	AgeSeconds int64  `json:"age_seconds,omitempty"`
	TTLSeconds int    `json:"ttl_seconds"`
}

// FeatureViewDetail is a feature view enriched with its store, freshness,
// current version and latest lineage.
type FeatureViewDetail struct {
	FeatureView
	Store         FeatureStoreSummary     `json:"store"`
	Freshness     FeatureFreshness        `json:"freshness"`
	Version       int                     `json:"version"`
	LastRun       *FeatureMaterialization `json:"last_materialization,omitempty"`
	FailureCount  int                     `json:"failure_count"`
	LatestFailure string                  `json:"latest_failure,omitempty"`
}

// StorageConnection is an external S3-compatible object store. Credentials
// are referenced by secret_ref (env:VAR holding ACCESS_KEY_ID:SECRET) and are
// never stored or returned.
type StorageConnection struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Endpoint        string           `json:"endpoint"`
	Region          string           `json:"region"`
	Bucket          string           `json:"bucket"`
	PathStyle       bool             `json:"path_style"`
	CABundle        string           `json:"ca_bundle,omitempty"`
	SecretRef       string           `json:"secret_ref"`
	SecretPresent   bool             `json:"secret_present"`
	AllowedProjects []string         `json:"allowed_projects"`
	Health          ConnectionHealth `json:"health"`
	CreatedBy       string           `json:"created_by"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type UpsertStorageConnectionRequest struct {
	Name            string   `json:"name"`
	Endpoint        string   `json:"endpoint"`
	Region          string   `json:"region"`
	Bucket          string   `json:"bucket"`
	PathStyle       bool     `json:"path_style"`
	CABundle        string   `json:"ca_bundle,omitempty"`
	SecretRef       string   `json:"secret_ref"`
	AllowedProjects []string `json:"allowed_projects"`
}
