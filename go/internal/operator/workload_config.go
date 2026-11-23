package operator

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Defaults for the cluster prerequisites the chart installs.
const (
	DefaultKEDAHTTPInterceptor = "keda-add-ons-http-interceptor-proxy.keda.svc.cluster.local:8080"
	// DefaultBuildKitImage is a rootless BuildKit release, pinned.
	DefaultBuildKitImage = "moby/buildkit:v0.34.1-rootless"
)

// WorkloadConfig is the operator configuration shared by the application,
// function and agent controllers. LoadWorkloadConfig reads it from the
// environment; the chart sets each variable.
type WorkloadConfig struct {
	// BaseDomain serves applications at <name>.<BaseDomain>
	// (KIONGA_APPLICATIONS_BASE_DOMAIN). Empty means no Ingress.
	BaseDomain string
	// TLSSecret is the wildcard certificate Secret in the workload namespace
	// (KIONGA_APPLICATIONS_TLS_SECRET). Empty serves plain HTTP.
	TLSSecret string
	// IngressClass is the ingressClassName (KIONGA_INGRESS_CLASS).
	IngressClass string
	// Interceptor is the KEDA HTTP add-on interceptor proxy Service,
	// host:port (KIONGA_KEDA_HTTP_INTERCEPTOR).
	InterceptorHost string
	InterceptorPort int32
	// SSOUpstream is the gateway Service that signs users in and proxies
	// visibility=sso hosts, host:port (KIONGA_SSO_UPSTREAM). Empty keeps sso
	// workloads off the Ingress.
	SSOUpstreamHost string
	SSOUpstreamPort int32
	// ImagePullSecrets are referenced by every workload pod
	// (KIONGA_IMAGE_PULL_SECRETS).
	ImagePullSecrets []corev1.LocalObjectReference
	Build            BuildConfig
}

// BuildConfig configures source builds.
type BuildConfig struct {
	// Registry is the repository prefix builds push to, for example
	// registry.example.com/kionga (KIONGA_BUILD_REGISTRY). Empty disables
	// source builds.
	Registry string
	// PushSecret is a kubernetes.io/dockerconfigjson Secret in the workload
	// namespace with push credentials (KIONGA_BUILD_PUSH_SECRET).
	PushSecret string
	// Image is the rootless BuildKit image (KIONGA_BUILDKIT_IMAGE).
	Image string
	// Insecure pushes over plain HTTP, for an in-cluster registry
	// (KIONGA_BUILD_REGISTRY_INSECURE=true).
	Insecure bool
}

// LoadWorkloadConfig reads WorkloadConfig from environment variables.
func LoadWorkloadConfig(getenv func(string) string) (WorkloadConfig, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	config := WorkloadConfig{
		BaseDomain:       strings.Trim(strings.TrimSpace(getenv("KIONGA_APPLICATIONS_BASE_DOMAIN")), "."),
		TLSSecret:        strings.TrimSpace(getenv("KIONGA_APPLICATIONS_TLS_SECRET")),
		IngressClass:     strings.TrimSpace(getenv("KIONGA_INGRESS_CLASS")),
		ImagePullSecrets: ParseImagePullSecrets(getenv("KIONGA_IMAGE_PULL_SECRETS")),
		Build: BuildConfig{
			Registry:   strings.TrimRight(strings.TrimSpace(getenv("KIONGA_BUILD_REGISTRY")), "/"),
			PushSecret: strings.TrimSpace(getenv("KIONGA_BUILD_PUSH_SECRET")),
			Image:      strings.TrimSpace(getenv("KIONGA_BUILDKIT_IMAGE")),
		},
	}
	if config.Build.Image == "" {
		config.Build.Image = DefaultBuildKitImage
	}
	if raw := strings.TrimSpace(getenv("KIONGA_BUILD_REGISTRY_INSECURE")); raw != "" {
		insecure, err := strconv.ParseBool(raw)
		if err != nil {
			return config, fmt.Errorf("KIONGA_BUILD_REGISTRY_INSECURE: %w", err)
		}
		config.Build.Insecure = insecure
	}
	interceptor := strings.TrimSpace(getenv("KIONGA_KEDA_HTTP_INTERCEPTOR"))
	if interceptor == "" {
		interceptor = DefaultKEDAHTTPInterceptor
	}
	var err error
	if config.InterceptorHost, config.InterceptorPort, err = hostPort(interceptor); err != nil {
		return config, fmt.Errorf("KIONGA_KEDA_HTTP_INTERCEPTOR: %w", err)
	}
	if upstream := strings.TrimSpace(getenv("KIONGA_SSO_UPSTREAM")); upstream != "" {
		if config.SSOUpstreamHost, config.SSOUpstreamPort, err = hostPort(upstream); err != nil {
			return config, fmt.Errorf("KIONGA_SSO_UPSTREAM: %w", err)
		}
	}
	return config, nil
}

func hostPort(value string) (string, int32, error) {
	host, rawPort, err := net.SplitHostPort(value)
	if err != nil {
		return "", 0, fmt.Errorf("want host:port, got %q", value)
	}
	port, err := strconv.ParseInt(rawPort, 10, 32)
	if err != nil || port < 1 || port > 65535 || host == "" {
		return "", 0, fmt.Errorf("want host:port, got %q", value)
	}
	return host, int32(port), nil
}
