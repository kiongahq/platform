package store

import (
	"testing"

	"github.com/kiongahq/platform/pkg/api"
)

func TestAPITokenLifecycle(t *testing.T) {
	repository := New()
	created, err := repository.CreateAPIToken("user-1", api.CreateAPITokenRequest{
		Name: "laptop cli", Services: []string{"projects"}, ProjectIDs: []string{"prj-demo"}, ExpiresInDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Secret == "" || created.Token.SecretHash == "" || created.Token.Prefix == "" {
		t.Fatalf("incomplete token: %+v", created)
	}
	resolved, err := repository.ResolveAPIToken(created.Secret)
	if err != nil || resolved.Subject != "user-1" {
		t.Fatalf("resolve: %+v %v", resolved, err)
	}
	if err := repository.RevokeAPIToken("user-1", created.Token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ResolveAPIToken(created.Secret); err == nil {
		t.Fatal("revoked token must not resolve")
	}
}

func TestAPITokenValidation(t *testing.T) {
	repository := New()
	for _, request := range []api.CreateAPITokenRequest{
		{Name: "x", Services: []string{"projects"}, ExpiresInDays: 30},
		{Name: "valid", Services: []string{"workbench"}, ExpiresInDays: 30},
		{Name: "valid", Services: []string{"projects"}, ExpiresInDays: 0},
	} {
		if _, err := repository.CreateAPIToken("user-1", request); err == nil {
			t.Fatalf("expected validation error for %+v", request)
		}
	}
}
