package httpapi

import (
	"log"

	"github.com/kiongahq/platform/internal/runtimeconfig"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// The rules live in runtimeconfig, which also loads these variables from files.
var (
	knowledgeSecrets  = runtimeconfig.KnowledgeSecrets
	connectionSecrets = runtimeconfig.ConnectionSecrets
	featureSecrets    = runtimeconfig.FeatureSecrets
	storageSecrets    = runtimeconfig.StorageSecrets
)

// refusedSecretRefs lists stored records whose reference no longer resolves, as
// "kind id variable". It holds names only, never values.
func refusedSecretRefs(data store.Repository) []string {
	refused := []string{}
	check := func(kind, id, ref string, scope runtimeconfig.SecretScope) {
		if ref != "" && ref != "none" && !scope.Permits(ref) {
			refused = append(refused, kind+" "+id+" "+ref)
		}
	}
	for _, item := range data.Connections() {
		check("connection", item.ID, item.SecretRef, runtimeconfig.PlatformSecrets(item.Type))
	}
	for _, item := range store.ListDocs[api.FeatureStoreConnection](data, store.FeatureStoreConnectionKind) {
		check("feature_store", item.ID, item.SecretRef, featureSecrets)
	}
	for _, item := range store.ListDocs[api.StorageConnection](data, store.StorageConnectionKind) {
		check("storage_connection", item.ID, item.SecretRef, storageSecrets)
	}
	for _, item := range store.ListDocs[knowledgeBase](data, knowledgeKind) {
		check("knowledge_base", item.ID, item.SecretRef, knowledgeSecrets)
		check("knowledge_base", item.ID, item.DSNRef, knowledgeSecrets)
	}
	for _, item := range store.ListDocs[agentAPIConnection](data, agentConnectionKind) {
		check("agent_api_connection", item.ID, item.SecretRef, connectionSecrets)
	}
	return refused
}

// LogRefusedSecretRefs reports at startup which records need migrating, so an
// operator finds them before a request fails. See ARCHITECTURE.md for the fix.
func LogRefusedSecretRefs(data store.Repository) {
	for _, item := range refusedSecretRefs(data) {
		log.Printf("secret reference refused, record will not authenticate until migrated: %s", item)
	}
}
