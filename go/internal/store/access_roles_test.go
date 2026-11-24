package store

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kiongahq/platform/pkg/api"
)

func TestUserRolesAcceptedAndRejected(t *testing.T) {
	s := New()
	for _, role := range UserRoles {
		if _, err := s.UpsertUserAccess("person-"+role, api.UpsertUserAccessRequest{Role: role}, "admin"); err != nil {
			t.Errorf("role %q from UserRoles was rejected: %v", role, err)
		}
	}
	for _, role := range []string{"service", "root", "Engineer"} {
		_, err := s.UpsertUserAccess("person-bad", api.UpsertUserAccessRequest{Role: role}, "admin")
		if err == nil || err.Error() != "role must be admin, operator, engineer, viewer or user" {
			t.Errorf("role %q must be rejected with the documented message, got %v", role, err)
		}
	}
	if slices.Contains(UserRoles, "service") {
		t.Error("service is a machine identity and must not be offered to people")
	}
}

// The console role list must equal the roles the gateway accepts when an
// account is provisioned, in the same order. The frontend list is parsed from
// source so a change on either side fails here.
func TestConsoleUserRolesMatchGateway(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "frontend", "features", "operations", "userRoles.ts"))
	if err != nil {
		t.Fatal(err)
	}
	entry := regexp.MustCompile(`\{\s*value:\s*'([^']+)',\s*label:\s*'([^']+)'\s*\}`)
	var values []string
	for _, match := range entry.FindAllStringSubmatch(string(source), -1) {
		if match[1] != match[2] {
			t.Errorf("label %q must equal value %q", match[2], match[1])
		}
		values = append(values, match[1])
	}
	if len(values) == 0 {
		t.Fatal("no roles parsed from userRoles.ts; the file format changed")
	}
	if !slices.Equal(values, UserRoles) {
		t.Fatalf("console roles %v differ from gateway roles %v", values, UserRoles)
	}
	if !strings.Contains(string(source), "DEFAULT_USER_ROLE: UserRole = 'user'") {
		t.Error("the console default role must stay user")
	}
}
