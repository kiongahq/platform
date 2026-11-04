package runtimeconfig

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// SecretScope limits which gateway environment variables a user-registered
// resource can reference. A resource pairs its reference with a caller-chosen
// endpoint, so an unrestricted env: lookup would hand any gateway secret
// (DATABASE_URL, MLAIOPS_INTERNAL_TOKEN) to whoever controls that endpoint.
//
// A variable is permitted when it carries the scope's dedicated prefix, is one
// of the names the scope has always documented, or is listed by the operator in
// the scope's allowlist variable (comma separated). The gateway's own secrets
// are refused whatever the configuration says.
type SecretScope struct {
	Prefix    string
	Names     []string
	Allowlist string
	// Exempt is a gateway secret this scope exists to use, such as the OpenFaaS
	// password on an OpenFaaS connection. It is the one exception to the refusal.
	Exempt string
}

var (
	KnowledgeSecrets = SecretScope{
		Prefix:    "KIONGA_KB_SECRET_",
		Names:     []string{"KIONGA_QDRANT_KEY", "KIONGA_KNOWLEDGE_DSN"},
		Allowlist: "KIONGA_KB_SECRET_REFS",
	}
	ConnectionSecrets = SecretScope{
		Prefix:    "KIONGA_CONNECTION_SECRET_",
		Names:     []string{"KIONGA_AGENT_API_TOKEN"},
		Allowlist: "KIONGA_CONNECTION_SECRET_REFS",
	}
	FeatureSecrets = SecretScope{
		Prefix:    "KIONGA_FEATURE_SECRET_",
		Names:     []string{"FEAST_TOKEN"},
		Allowlist: "KIONGA_FEATURE_SECRET_REFS",
	}
	StorageSecrets = SecretScope{
		Prefix:    "KIONGA_STORAGE_SECRET_",
		Names:     []string{"LAKE_CREDENTIALS"},
		Allowlist: "KIONGA_STORAGE_SECRET_REFS",
	}
)

// PlatformSecrets is the scope for a connection of one service type. Only that
// service's own integration secret is exempt, so a Prefect connection cannot read
// the OpenFaaS password.
func PlatformSecrets(connectionType string) SecretScope {
	scope := SecretScope{Prefix: "KIONGA_PLATFORM_SECRET_", Allowlist: "KIONGA_PLATFORM_SECRET_REFS"}
	switch connectionType {
	case "openfaas":
		scope.Exempt = "OPENFAAS_PASSWORD"
	case "mlflow":
		scope.Exempt = "MLFLOW_TOKEN"
	case "kfp":
		scope.Exempt = "KFP_TOKEN"
	}
	return scope
}

func allScopes() []SecretScope {
	return []SecretScope{KnowledgeSecrets, ConnectionSecrets, FeatureSecrets, StorageSecrets, PlatformSecrets("")}
}

// GatewayOwnSecret reports whether name is a secret the gateway holds for its own
// use, which no resource may reference even when an allowlist names it.
func GatewayOwnSecret(name string) bool {
	if slices.Contains(SecretNames, name) {
		return true
	}
	return name == "KIONGA_DESIGNER_RUNNER_TOKEN" || name == "KIONGA_SANDBOX_TOKEN"
}

// configurationName reports names that hold settings about references rather than a
// secret: the allowlists, which match their own prefix, and any *_FILE path.
func configurationName(name string) bool {
	if strings.HasSuffix(name, "_FILE") {
		return true
	}
	for _, scope := range allScopes() {
		if name == scope.Allowlist {
			return true
		}
	}
	return false
}

// PermitsName reports whether this scope may read the variable called name.
func (scope SecretScope) PermitsName(name string) bool {
	if name != "" && name == scope.Exempt {
		return true
	}
	if !keyPattern.MatchString(name) || GatewayOwnSecret(name) || configurationName(name) {
		return false
	}
	if len(name) > len(scope.Prefix) && strings.HasPrefix(name, scope.Prefix) {
		return true
	}
	if slices.Contains(scope.Names, name) {
		return true
	}
	for _, allowed := range strings.Split(os.Getenv(scope.Allowlist), ",") {
		if strings.TrimSpace(allowed) == name {
			return true
		}
	}
	return false
}

// Permits reports whether ref is an env: reference this scope may resolve.
func (scope SecretScope) Permits(ref string) bool {
	name, ok := strings.CutPrefix(ref, "env:")
	return ok && scope.PermitsName(name)
}

// Resolve returns the variable's value, or ok false for a refused reference.
// Records stored before this restriction existed are checked here too, so a saved
// reference to a gateway secret stops resolving rather than continuing to send it.
func (scope SecretScope) Resolve(ref string) (value string, ok bool) {
	if !scope.Permits(ref) {
		return "", false
	}
	return os.Getenv(strings.TrimPrefix(ref, "env:")), true
}

// Value is Resolve for callers that treat a refused reference as an absent one.
func (scope SecretScope) Value(ref string) string {
	value, _ := scope.Resolve(ref)
	return value
}

// loadReferenceFiles delivers permitted reference variables from <NAME>_FILE, so
// secret-file deployments can use them. A gateway secret is never loaded this way,
// whatever an allowlist says; those have their own loop over SecretNames.
func loadReferenceFiles() error {
	for _, item := range os.Environ() {
		key, path, _ := strings.Cut(item, "=")
		name, isFile := strings.CutSuffix(key, "_FILE")
		if !isFile || path == "" || GatewayOwnSecret(name) {
			continue
		}
		permitted := false
		for _, scope := range allScopes() {
			permitted = permitted || scope.PermitsName(name)
		}
		if !permitted {
			continue
		}
		if os.Getenv(name) != "" {
			return fmt.Errorf("set only one of %s and %s_FILE", name, name)
		}
		data, err := readFile(path)
		if err != nil {
			return fmt.Errorf("cannot read %s_FILE", name)
		}
		value := strings.TrimRight(string(data), "\r\n")
		if value == "" {
			return fmt.Errorf("%s_FILE is empty", name)
		}
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("could not load %s_FILE", name)
		}
	}
	return nil
}
