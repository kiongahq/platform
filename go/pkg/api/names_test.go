package api

import (
	"strings"
	"testing"

	validation "k8s.io/apimachinery/pkg/util/validation"
)

func TestAgentDNSNameIsStableSafeAndCollisionResistant(t *testing.T) {
	first := AgentDNSName("agt-123")
	if first != AgentDNSName("agt-123") {
		t.Fatal("agent DNS name must be stable")
	}
	if errors := validation.IsDNS1123Subdomain(first); len(errors) != 0 {
		t.Fatalf("agent DNS name %q is invalid: %v", first, errors)
	}
	if len(first) > 63 || !strings.HasPrefix(first, agentDNSPrefix) {
		t.Fatalf("unexpected agent DNS name %q", first)
	}
	if AgentDNSName("tenant/agent") == AgentDNSName("tenant-agent") {
		t.Fatal("normalization must not collapse distinct immutable IDs")
	}
	long := AgentDNSName(strings.Repeat("very-long-agent-id-", 8))
	if len(long) > 63 || len(validation.IsDNS1123Subdomain(long)) != 0 {
		t.Fatalf("long ID produced invalid DNS name %q", long)
	}
}
