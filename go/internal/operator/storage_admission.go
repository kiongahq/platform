package operator

import (
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// ValidateTenantStorage is the admission check for tenant workload storage:
// tenant pods may mount only PersistentVolumeClaims, ConfigMaps, Secrets,
// emptyDir, projected and downward-API volumes, never hostPath (or any
// other node- or driver-level volume source), and a claim's StorageClass must
// be on the operator's allowlist when one is configured. External object
// storage reaches tenants through CSI-backed claims of an allowlisted class,
// so a tenant can never choose where on a node its data lands.
func ValidateTenantStorage(storageClass string, allowedClasses []string, volumes []corev1.Volume) error {
	if len(allowedClasses) > 0 && !slices.Contains(allowedClasses, storageClass) {
		shown := storageClass
		if shown == "" {
			shown = "(cluster default)"
		}
		return fmt.Errorf("storage class %s is not allowlisted for tenant workloads (allowed: %s)", shown, strings.Join(allowedClasses, ", "))
	}
	for _, volume := range volumes {
		source := volume.VolumeSource
		switch {
		case source.HostPath != nil:
			return fmt.Errorf("volume %s uses hostPath, which is never allowed for tenant workloads", volume.Name)
		case source.PersistentVolumeClaim != nil, source.ConfigMap != nil, source.Secret != nil,
			source.EmptyDir != nil, source.Projected != nil, source.DownwardAPI != nil:
		default:
			return fmt.Errorf("volume %s uses a source tenant workloads may not mount directly; use a claim of an allowlisted storage class", volume.Name)
		}
	}
	return nil
}

// ParseStorageClasses reads a comma-separated allowlist.
func ParseStorageClasses(value string) []string {
	out := []string{}
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
