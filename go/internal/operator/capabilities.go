package operator

import (
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

// Optional cluster APIs. The operator detects them once at startup and
// degrades with a status condition when one is missing instead of crashing.
var (
	HTTPScaledObjectGVK = schema.GroupVersionKind{Group: "http.keda.sh", Version: "v1alpha1", Kind: "HTTPScaledObject"}
	InferenceServiceGVK = schema.GroupVersionKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"}
)

// Capabilities records which optional APIs the cluster serves.
type Capabilities struct {
	// KEDAHTTP is true when the KEDA HTTP add-on serves HTTPScaledObject:
	// requests-based scaling and scale to zero.
	KEDAHTTP bool
	// KServe is true when KServe serves InferenceService.
	KServe bool
}

func (c Capabilities) String() string {
	return fmt.Sprintf("keda-http=%t kserve=%t", c.KEDAHTTP, c.KServe)
}

// DetectCapabilities asks the API server's discovery endpoint which optional
// APIs exist. A group that is not served is not an error; any other
// discovery failure is, so a flaky API server does not silently turn
// features off.
func DetectCapabilities(client discovery.DiscoveryInterface) (Capabilities, error) {
	var capabilities Capabilities
	var err error
	if capabilities.KEDAHTTP, err = serves(client, HTTPScaledObjectGVK, "httpscaledobjects"); err != nil {
		return capabilities, err
	}
	if capabilities.KServe, err = serves(client, InferenceServiceGVK, "inferenceservices"); err != nil {
		return capabilities, err
	}
	return capabilities, nil
}

func serves(client discovery.DiscoveryInterface, gvk schema.GroupVersionKind, plural string) (bool, error) {
	resources, err := client.ServerResourcesForGroupVersion(gvk.GroupVersion().String())
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	var groupErr *discovery.ErrGroupDiscoveryFailed
	if errors.As(err, &groupErr) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("discover %s: %w", gvk.GroupVersion(), err)
	}
	for _, resource := range resources.APIResources {
		if resource.Name == plural && resource.Kind == gvk.Kind {
			return true, nil
		}
	}
	return false, nil
}
