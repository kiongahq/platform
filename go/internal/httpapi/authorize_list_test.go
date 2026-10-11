package httpapi

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/policy"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// countingStore counts single-project lookups, which hit Postgres once each.
type countingStore struct {
	*store.Store
	lookups atomic.Int64
}

func (c *countingStore) Project(id string) (api.Project, error) {
	c.lookups.Add(1)
	return c.Store.Project(id)
}

func TestListingProjectsDoesNotLookUpEachProject(t *testing.T) {
	repository := &countingStore{Store: store.New()}
	seeded := len(repository.Projects())
	var last api.Project
	for i := range 50 {
		var err error
		if last, err = repository.CreateProject(api.CreateProjectRequest{Name: fmt.Sprintf("p-%02d", i), Template: "tabular-classification"}, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	admin := auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}}
	visible := newAuthorizer(repository, admin, policy.Context{}).visibleProjects(repository.Projects())
	if len(visible) != seeded+50 {
		t.Fatalf("admin sees %d of %d projects", len(visible), seeded+50)
	}
	if n := repository.lookups.Load(); n != 0 {
		t.Fatalf("listing 50 projects made %d single-project lookups, want 0 (N+1)", n)
	}
	// Tags still come from the project for resource_tags conditions.
	tags := newAuthorizer(repository, admin, policy.Context{}).resourceTags(policy.ProjectResource(last.ID))
	if tags["template"] == "" || tags["template"] != last.Template {
		t.Fatalf("tags = %v", tags)
	}
}
