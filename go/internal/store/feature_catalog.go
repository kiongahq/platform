package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/kiongahq/platform/pkg/api"
)

// Feature-store documents: external connections, immutable definition
// versions and materialization lineage. All build on Documents.
const (
	FeatureStoreConnectionKind   = "feature_store_connection"
	StorageConnectionKind        = "storage_connection"
	FeatureDefinitionVersionKind = "feature_definition_version"
	featureDefinitionHeadKind    = "feature_definition_head"
	FeatureMaterializationKind   = "feature_materialization"
)

// NewResourceID returns a unique, prefixed identifier.
func NewResourceID(prefix string) string { return id(prefix) }

// SaveFeatureStoreConnection creates (create=true) or replaces a connection.
// Create refuses an existing id; update refuses a missing one.
func SaveFeatureStoreConnection(docs Documents, connection api.FeatureStoreConnection, create bool, actor string) (api.FeatureStoreConnection, error) {
	now := time.Now().UTC()
	action := "feature_store_connection.updated"
	if create {
		action = "feature_store_connection.created"
	}
	return UpdateDoc(docs, FeatureStoreConnectionKind, connection.ID, func(current api.FeatureStoreConnection, exists bool) (api.FeatureStoreConnection, error) {
		if create && exists {
			return current, ErrConflict
		}
		if !create && !exists {
			return current, ErrNotFound
		}
		if exists {
			connection.CreatedAt, connection.CreatedBy = current.CreatedAt, current.CreatedBy
			if connection.Health.State == "" {
				connection.Health = current.Health
			}
		} else {
			connection.CreatedAt, connection.CreatedBy = now, actorOrAnonymous(actor)
		}
		connection.UpdatedAt = now
		connection.SecretPresent = false // resolved at read time, never persisted
		return connection, nil
	}, action, actor)
}

// SetFeatureStoreHealth records a check result.
func SetFeatureStoreHealth(docs Documents, connectionID string, health api.ConnectionHealth, actor string) (api.FeatureStoreConnection, error) {
	return UpdateDoc(docs, FeatureStoreConnectionKind, connectionID, func(current api.FeatureStoreConnection, exists bool) (api.FeatureStoreConnection, error) {
		if !exists {
			return current, ErrNotFound
		}
		current.Health = health
		return current, nil
	}, "feature_store_connection.tested", actor)
}

// SaveStorageConnection creates or replaces an external storage connection.
func SaveStorageConnection(docs Documents, connection api.StorageConnection, create bool, actor string) (api.StorageConnection, error) {
	now := time.Now().UTC()
	action := "storage_connection.updated"
	if create {
		action = "storage_connection.created"
	}
	return UpdateDoc(docs, StorageConnectionKind, connection.ID, func(current api.StorageConnection, exists bool) (api.StorageConnection, error) {
		if create && exists {
			return current, ErrConflict
		}
		if !create && !exists {
			return current, ErrNotFound
		}
		if exists {
			connection.CreatedAt, connection.CreatedBy = current.CreatedAt, current.CreatedBy
			if connection.Health.State == "" {
				connection.Health = current.Health
			}
		} else {
			connection.CreatedAt, connection.CreatedBy = now, actorOrAnonymous(actor)
		}
		connection.UpdatedAt = now
		connection.SecretPresent = false
		return connection, nil
	}, action, actor)
}

// SetStorageHealth records a storage connection check result.
func SetStorageHealth(docs Documents, connectionID string, health api.ConnectionHealth, actor string) (api.StorageConnection, error) {
	return UpdateDoc(docs, StorageConnectionKind, connectionID, func(current api.StorageConnection, exists bool) (api.StorageConnection, error) {
		if !exists {
			return current, ErrNotFound
		}
		current.Health = health
		return current, nil
	}, "storage_connection.tested", actor)
}

type featureDefinitionHead struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	SHA256  string `json:"sha256"`
}

// FeatureDefinitionHash is the content hash of the parts of a view that
// change its meaning. Status and counters are excluded.
func FeatureDefinitionHash(view api.FeatureView, storeID string) string {
	payload, _ := json.Marshal(struct {
		Entity     string             `json:"entity"`
		Fields     []api.FeatureField `json:"fields"`
		Tags       []string           `json:"tags"`
		Source     string             `json:"source"`
		TTLSeconds int                `json:"ttl_seconds"`
		StoreID    string             `json:"store_id"`
	}{view.Entity, view.Fields, view.Tags, view.Source, view.TTLSeconds, storeID})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func featureVersionID(name string, version int) string { return fmt.Sprintf("%s@%d", name, version) }

// RecordFeatureDefinitionVersion writes a new immutable version when the
// definition changed and returns the current version. created reports
// whether a new version was written.
func RecordFeatureDefinitionVersion(docs Documents, view api.FeatureView, storeID, actor string) (api.FeatureDefinitionVersion, bool, error) {
	if storeID == "" {
		storeID = "internal"
	}
	sha := FeatureDefinitionHash(view, storeID)
	head, err := UpdateDoc(docs, featureDefinitionHeadKind, view.Name, func(current featureDefinitionHead, exists bool) (featureDefinitionHead, error) {
		if exists && current.SHA256 == sha {
			return current, ErrSkipWrite
		}
		return featureDefinitionHead{Name: view.Name, Version: current.Version + 1, SHA256: sha}, nil
	}, "", actor)
	if err != nil {
		return api.FeatureDefinitionVersion{}, false, err
	}
	version := api.FeatureDefinitionVersion{
		ID: featureVersionID(view.Name, head.Version), Name: view.Name, Version: head.Version, SHA256: sha,
		Entity: view.Entity, Fields: append([]api.FeatureField(nil), view.Fields...), Tags: append([]string(nil), view.Tags...),
		Source: view.Source, TTLSeconds: view.TTLSeconds, StoreID: storeID,
		CreatedBy: actorOrAnonymous(actor), CreatedAt: time.Now().UTC(),
	}
	created := true
	stored, err := UpdateDoc(docs, FeatureDefinitionVersionKind, version.ID, func(current api.FeatureDefinitionVersion, exists bool) (api.FeatureDefinitionVersion, error) {
		if exists {
			created = false
			if current.SHA256 == sha {
				return current, ErrSkipWrite
			}
			// Immutable: an existing version is never overwritten.
			return current, ErrConflict
		}
		return version, nil
	}, "feature_definition.version_created", actor)
	return stored, created, err
}

// FeatureDefinitionVersions lists a view's versions, newest first.
func FeatureDefinitionVersions(docs Documents, name string) []api.FeatureDefinitionVersion {
	out := []api.FeatureDefinitionVersion{}
	for _, version := range ListDocs[api.FeatureDefinitionVersion](docs, FeatureDefinitionVersionKind) {
		if version.Name == name {
			out = append(out, version)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out
}

// RecordFeatureMaterialization appends one lineage record.
func RecordFeatureMaterialization(docs Documents, record api.FeatureMaterialization, actor string) (api.FeatureMaterialization, error) {
	if record.ID == "" {
		record.ID = id("fmat")
	}
	record.CreatedAt = time.Now().UTC()
	record.ReportedBy = actorOrAnonymous(actor)
	action := "feature_view.materialized"
	if record.Status == "failed" {
		action = "feature_view.materialization_failed"
	}
	return UpdateDoc(docs, FeatureMaterializationKind, record.ID, func(current api.FeatureMaterialization, exists bool) (api.FeatureMaterialization, error) {
		if exists {
			return current, ErrConflict
		}
		return record, nil
	}, action, actor)
}

// FeatureMaterializations lists a view's lineage records, newest first.
func FeatureMaterializations(docs Documents, view string) []api.FeatureMaterialization {
	out := []api.FeatureMaterialization{}
	for _, record := range ListDocs[api.FeatureMaterialization](docs, FeatureMaterializationKind) {
		if view == "" || record.View == view {
			out = append(out, record)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
