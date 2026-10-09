package storetest

import (
	"errors"
	"sync"
	"testing"

	"github.com/kiongahq/platform/internal/policy"
	"github.com/kiongahq/platform/internal/store"
)

// IAM checks group, policy, revision and attachment persistence on any
// backend: versions bump on every update, revisions are immutable, stale
// writes conflict, attached policies and groups cannot be deleted, and every
// change is audited.
func IAM(t *testing.T, repo store.Repository) {
	t.Helper()
	auditBefore := len(repo.Audit())
	group, err := store.SaveGroup(repo, store.Group{ID: "ml-team", Name: "ML team", Members: []string{"alice", "alice", " bob "}}, true, "admin")
	if err != nil || len(group.Members) != 2 {
		t.Fatalf("create group: %+v %v", group, err)
	}
	if _, err := store.SaveGroup(repo, store.Group{ID: "ml-team", Name: "dup"}, true, "admin"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate group: %v", err)
	}
	if _, err := store.SaveGroup(repo, store.Group{ID: "missing", Name: "x"}, false, "admin"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update missing group: %v", err)
	}
	if got := store.GroupsFor(repo, "bob"); len(got) != 1 || got[0] != "ml-team" {
		t.Fatalf("membership: %v", got)
	}

	p := policy.Policy{ID: "read-p1", Name: "Read p1", Statements: []policy.Statement{{Sid: "Read", Effect: policy.Allow, Actions: []string{policy.PipelineRead}, Resources: []string{"kionga:project/p1/*"}}}}
	created, err := store.CreatePolicy(repo, p, "admin")
	if err != nil || created.Version != 1 || created.CreatedBy != "admin" {
		t.Fatalf("create policy: %+v %v", created, err)
	}
	if _, err := store.CreatePolicy(repo, p, "admin"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate policy: %v", err)
	}
	// Concurrent updates must each produce their own version and revision.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			next := p
			next.Description = "concurrent"
			if _, err := store.UpdatePolicy(repo, "read-p1", next, 0, "admin"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	current, err := store.GetDoc[policy.Policy](repo, store.PolicyKind, "read-p1")
	if err != nil || current.Version != 9 {
		t.Fatalf("lost policy updates: %+v %v", current, err)
	}
	if revisions := store.PolicyRevisions(repo, "read-p1"); len(revisions) != 9 || revisions[0].Version != 9 || revisions[8].Version != 1 {
		t.Fatalf("revisions: %d", len(revisions))
	}
	if _, err := store.UpdatePolicy(repo, "read-p1", p, 3, "admin"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale update must conflict: %v", err)
	}
	first, err := store.GetPolicyRevision(repo, "read-p1", 1)
	if err != nil || first.Policy.Description != "" || first.Author != "admin" {
		t.Fatalf("revision 1 must be the original snapshot: %+v %v", first, err)
	}

	attachment, err := store.Attach(repo, "read-p1", store.PrincipalGroup, "ml-team", "admin")
	if err != nil || attachment.ID != store.AttachmentID("read-p1", "group", "ml-team") {
		t.Fatalf("attach: %+v %v", attachment, err)
	}
	if _, err := store.Attach(repo, "read-p1", store.PrincipalGroup, "ml-team", "admin"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("double attach: %v", err)
	}
	if _, err := store.Attach(repo, "read-p1", "robot", "x", "admin"); err == nil {
		t.Fatal("unknown principal type must fail")
	}
	groups, bindings := store.AttachedBindings(repo, "alice")
	if len(groups) != 1 || len(bindings) != 1 || bindings[0].Source != "group:ml-team" || bindings[0].Policy.Version != 9 {
		t.Fatalf("bindings: %v %+v", groups, bindings)
	}
	if _, bindings := store.AttachedBindings(repo, "carol"); len(bindings) != 0 {
		t.Fatalf("non-member must have no bindings: %+v", bindings)
	}
	if err := store.DeletePolicy(repo, "read-p1", "admin"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("deleting an attached policy must conflict: %v", err)
	}
	if err := store.DeleteGroup(repo, "ml-team", "admin"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("deleting a group with attachments must conflict: %v", err)
	}
	if err := store.Detach(repo, attachment.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeletePolicy(repo, "read-p1", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteGroup(repo, "ml-team", "admin"); err != nil {
		t.Fatal(err)
	}
	if len(store.PolicyRevisions(repo, "read-p1")) != 9 {
		t.Fatal("revisions are history and survive deletion")
	}
	actions := map[string]bool{}
	for _, event := range repo.Audit() {
		actions[event.Action] = true
	}
	for _, want := range []string{"iam.group.created", "iam.policy.created", "iam.policy.updated", "iam.policy_revision.created", "iam.policy.attached", "iam.policy.detached", "iam.policy.deleted", "iam.group.deleted"} {
		if !actions[want] {
			t.Errorf("missing audit event %s", want)
		}
	}
	if len(repo.Audit()) <= auditBefore {
		t.Fatal("IAM changes must be audited")
	}
}
