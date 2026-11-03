package httpapi

import (
	"errors"
	"os"
	"strings"

	"github.com/kiongahq/platform/internal/runtimeconfig"
)

// secretScope limits which gateway environment variables a user-registered
// resource can reference. A resource pairs its reference with a caller-chosen
// endpoint, so an unrestricted env: lookup would hand any gateway secret
// (DATABASE_URL, MLAIOPS_INTERNAL_TOKEN) to whoever controls that endpoint.
//
// A variable is permitted when it carries the scope's dedicated prefix, is one
// of the names the scope has always documented, or is listed by the operator in
// the scope's allowlist variable (comma separated). The gateway's own secrets
// are refused whatever the configuration says.
type secretScope struct {
	prefix    string
	names     []string
	allowlist string
}

var (
	knowledgeSecrets = secretScope{
		prefix:    "KIONGA_KB_SECRET_",
		names:     []string{"KIONGA_QDRANT_KEY", "KIONGA_KNOWLEDGE_DSN"},
		allowlist: "KIONGA_KB_SECRET_REFS",
	}
	connectionSecrets = secretScope{
		prefix:    "KIONGA_CONNECTION_SECRET_",
		names:     []string{"KIONGA_AGENT_API_TOKEN"},
		allowlist: "KIONGA_CONNECTION_SECRET_REFS",
	}
)

var errSecretRefNotPermitted = errors.New("secret reference is not permitted")

// gatewayOwnSecret reports whether name is a secret the gateway holds for its
// own use, which no resource may reference even when an allowlist names it.
func gatewayOwnSecret(name string) bool {
	for _, own := range runtimeconfig.SecretNames {
		if name == own {
			return true
		}
	}
	switch name {
	case "KIONGA_DESIGNER_RUNNER_TOKEN", "KIONGA_SANDBOX_TOKEN":
		return true
	}
	return false
}

// permits reports whether ref is an env: reference this scope may resolve.
func (scope secretScope) permits(ref string) bool {
	if !knowledgeRef.MatchString(ref) {
		return false
	}
	name := strings.TrimPrefix(ref, "env:")
	if gatewayOwnSecret(name) {
		return false
	}
	if len(name) > len(scope.prefix) && strings.HasPrefix(name, scope.prefix) {
		return true
	}
	for _, allowed := range scope.names {
		if name == allowed {
			return true
		}
	}
	for _, allowed := range strings.Split(os.Getenv(scope.allowlist), ",") {
		if strings.TrimSpace(allowed) == name {
			return true
		}
	}
	return false
}

// resolve returns the variable's value. Records stored before this restriction
// existed are checked here too, so a saved reference to a gateway secret stops
// resolving rather than continuing to send it.
func (scope secretScope) resolve(ref string) (string, error) {
	if !scope.permits(ref) {
		return "", errSecretRefNotPermitted
	}
	return os.Getenv(strings.TrimPrefix(ref, "env:")), nil
}
