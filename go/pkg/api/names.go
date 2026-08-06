package api

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

const agentDNSPrefix = "kionga-agent-"

var nonDNSCharacter = regexp.MustCompile(`[^a-z0-9]+`)

// AgentDNSName maps an immutable control-plane agent ID to the Kubernetes
// resource name used by the dispatcher, operator, and gateway. The digest is
// always present so IDs that normalize to the same DNS label remain distinct.
func AgentDNSName(agentID string) string {
	raw := strings.TrimSpace(agentID)
	stem := strings.Trim(nonDNSCharacter.ReplaceAllString(strings.ToLower(raw), "-"), "-")
	if stem == "" {
		stem = "resource"
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))[:10]
	maximumStem := 63 - len(agentDNSPrefix) - len(digest) - 1
	if len(stem) > maximumStem {
		stem = strings.Trim(stem[:maximumStem], "-")
	}
	if stem == "" {
		stem = "resource"
	}
	return agentDNSPrefix + stem + "-" + digest
}
