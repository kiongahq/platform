package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kiongahq/platform/pkg/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// Resolve on every invocation using the existing in-cluster identity and named
// Secret GET pattern used by workspaces. No list/watch or RBAC expansion.
func agentRuntimeToken(ctx context.Context, agentID string) (string, error) {
	if strings.TrimSpace(os.Getenv("AGENT_RUNTIME_URL")) != "" {
		// Compose explicitly configures its separate runtime credential.
		// Never fall back to the gateway's service credential.
		return os.Getenv("KIONGA_AGENT_RUNTIME_TOKEN"), nil
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		return "", err
	}
	config.Timeout = 3 * time.Second
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return "", err
	}
	namespace := strings.TrimSpace(os.Getenv("KIONGA_AGENT_NAMESPACE"))
	if namespace == "" {
		namespace = "default"
	}
	return discoverAgentRuntimeToken(ctx, client, namespace, agentID)
}

func discoverAgentRuntimeToken(ctx context.Context, client dynamic.Interface, namespace, agentID string) (string, error) {
	secret, err := client.Resource(secretResource).Namespace(namespace).Get(ctx, api.AgentRuntimeSecretName(agentID), metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	encoded, _, err := unstructured.NestedString(secret.Object, "data", "KIONGA_INTERNAL_TOKEN")
	if err != nil {
		return "", err
	}
	token, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(token) < 32 {
		return "", fmt.Errorf("agent runtime credential is missing or invalid")
	}
	return string(token), nil
}
