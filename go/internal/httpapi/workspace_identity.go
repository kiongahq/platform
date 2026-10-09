package httpapi

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/kiongahq/platform/internal/auth"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var workspaceResource = schema.GroupVersionResource{Group: "mlaiops.io", Version: "v1alpha1", Resource: "kiongaworkspaces"}
var secretResource = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// WorkspaceIdentity authenticates only requests arriving on the internal-only
// workspace API listener. It never trusts a caller-supplied subject or role.
// The ordinary RBAC resolver still checks current grants on every request.
func WorkspaceIdentity(next http.Handler, client dynamic.Interface, namespace, localToken, localSubject string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			http.Error(w, "workspace API request rejected", http.StatusForbidden)
			return
		}
		name := r.Header.Get("X-Kionga-Workspace-Name")
		token := r.Header.Get("X-Kionga-Workspace-Token")
		if token == "" || len(token) > 256 || len(name) > 253 {
			http.Error(w, "workspace credential required", http.StatusUnauthorized)
			return
		}
		var subject string
		if namespace != "" {
			if client == nil || name == "" {
				http.Error(w, "workspace credential rejected", http.StatusUnauthorized)
				return
			}
			workspace, err := client.Resource(workspaceResource).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
			if err != nil {
				http.Error(w, "workspace credential rejected", http.StatusUnauthorized)
				return
			}
			disabled, _, _ := unstructured.NestedBool(workspace.Object, "spec", "disabled")
			services, _, _ := unstructured.NestedStringSlice(workspace.Object, "spec", "services")
			subject, _, _ = unstructured.NestedString(workspace.Object, "spec", "subject")
			if disabled || subject == "" || (!slices.Contains(services, "workbench") && !slices.Contains(services, "ide")) {
				http.Error(w, "workspace access revoked", http.StatusForbidden)
				return
			}
			secret, err := client.Resource(secretResource).Namespace(namespace).Get(r.Context(), name+"-auth", metav1.GetOptions{})
			if err != nil {
				http.Error(w, "workspace credential rejected", http.StatusUnauthorized)
				return
			}
			if secret.GetAnnotations()["mlaiops.io/subject"] != subject {
				http.Error(w, "workspace identity is being updated", http.StatusForbidden)
				return
			}
			encoded, _, _ := unstructured.NestedString(secret.Object, "data", "api-token")
			expected, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || len(expected) < 32 || subtle.ConstantTimeCompare([]byte(token), expected) != 1 {
				http.Error(w, "workspace credential rejected", http.StatusUnauthorized)
				return
			}
		} else {
			// Shared Compose workspaces exist only in the single-user local profile.
			if os.Getenv("OIDC_ISSUER") != "" || os.Getenv("KIONGA_ENVIRONMENT") == "production" || localToken == "" || name != "local" || subtle.ConstantTimeCompare([]byte(token), []byte(localToken)) != 1 {
				http.Error(w, "workspace credential rejected", http.StatusUnauthorized)
				return
			}
			subject = localSubject
		}
		r.Header.Del("X-Kionga-Workspace-Name")
		r.Header.Del("X-Kionga-Workspace-Token")
		roles := []string{auth.RoleUser}
		if namespace == "" {
			roles = []string{auth.RoleAdmin}
		}
		principal := auth.Principal{Subject: subject, Roles: roles, Credential: "workspace"}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	})
}
