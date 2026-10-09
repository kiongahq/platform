package store

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/policy"
)

// IAM resources are documents: groups, versioned policies, immutable policy
// revisions, and attachments binding a policy to a user or a group. Every
// mutation is audited by the Documents backend in the same transaction.
const (
	GroupKind            = "group"
	PolicyKind           = "policy"
	PolicyRevisionKind   = "policy_revision"
	PolicyAttachmentKind = "policy_attachment"
)

// Principal types a policy can be attached to.
const (
	PrincipalUser  = "user"
	PrincipalGroup = "group"
)

// Group is a named set of subjects that policies can be attached to.
type Group struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Members     []string  `json:"members"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	UpdatedBy   string    `json:"updated_by,omitempty"`
}

// PolicyRevision is an immutable snapshot of one policy version.
type PolicyRevision struct {
	PolicyID  string        `json:"policy_id"`
	Version   int           `json:"version"`
	Policy    policy.Policy `json:"policy"`
	Author    string        `json:"author"`
	CreatedAt time.Time     `json:"created_at"`
}

// PolicyAttachment binds a policy to a user or a group.
type PolicyAttachment struct {
	ID            string    `json:"id"`
	PolicyID      string    `json:"policy_id"`
	PrincipalType string    `json:"principal_type"`
	PrincipalID   string    `json:"principal_id"`
	CreatedAt     time.Time `json:"created_at"`
	CreatedBy     string    `json:"created_by"`
}

// AttachmentID is deterministic, so attaching twice is a conflict rather
// than a duplicate.
func AttachmentID(policyID, principalType, principalID string) string {
	return policyID + ":" + principalType + ":" + principalID
}

// ---- groups -----------------------------------------------------------------

// Groups lists every group sorted by ID.
func Groups(docs Documents) []Group {
	groups := ListDocs[Group](docs, GroupKind)
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	return groups
}

// GroupsFor returns the IDs of the groups subject belongs to.
func GroupsFor(docs Documents, subject string) []string {
	out := []string{}
	for _, group := range Groups(docs) {
		if slices.Contains(group.Members, subject) {
			out = append(out, group.ID)
		}
	}
	return out
}

// SaveGroup creates (create=true) or replaces a group's name, description
// and members. Creating an existing ID is ErrConflict; updating a missing
// one is ErrNotFound.
func SaveGroup(docs Documents, group Group, create bool, actor string) (Group, error) {
	action := "iam.group.updated"
	if create {
		action = "iam.group.created"
	}
	return UpdateDoc(docs, GroupKind, group.ID, func(current Group, exists bool) (Group, error) {
		if create && exists {
			return current, fmt.Errorf("%w: group %s already exists", ErrConflict, group.ID)
		}
		if !create && !exists {
			return current, ErrNotFound
		}
		now := time.Now().UTC()
		group.CreatedAt, group.UpdatedAt, group.UpdatedBy = now, now, actorOrAnonymous(actor)
		if exists {
			group.CreatedAt = current.CreatedAt
		}
		group.Members = unique(group.Members)
		return group, nil
	}, action, actor)
}

// DeleteGroup removes a group that has no policy attachments.
func DeleteGroup(docs Documents, id, actor string) error {
	if n := len(AttachmentsFor(docs, PrincipalGroup, id)); n > 0 {
		return fmt.Errorf("%w: group %s has %d policy attachments; detach them first", ErrConflict, id, n)
	}
	return docs.DeleteDocument(GroupKind, id, "iam.group.deleted", actor)
}

// ---- policies -----------------------------------------------------------------

// Policies lists customer-managed policies sorted by ID.
func Policies(docs Documents) []policy.Policy {
	items := ListDocs[policy.Policy](docs, PolicyKind)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

// CreatePolicy stores version 1 of a validated policy and its revision.
func CreatePolicy(docs Documents, p policy.Policy, actor string) (policy.Policy, error) {
	saved, err := UpdateDoc(docs, PolicyKind, p.ID, func(current policy.Policy, exists bool) (policy.Policy, error) {
		if exists {
			return current, fmt.Errorf("%w: policy %s already exists", ErrConflict, p.ID)
		}
		now := time.Now().UTC()
		p.Version, p.Managed = 1, false
		p.CreatedAt, p.UpdatedAt = now, now
		p.CreatedBy, p.UpdatedBy = actorOrAnonymous(actor), actorOrAnonymous(actor)
		return p, nil
	}, "iam.policy.created", actor)
	if err != nil {
		return saved, err
	}
	return saved, recordPolicyRevision(docs, saved, actor)
}

// UpdatePolicy replaces a policy's content and bumps its version. When
// expectedVersion is non-zero it must equal the current version, so two
// editors cannot silently overwrite each other (ErrConflict).
func UpdatePolicy(docs Documents, id string, p policy.Policy, expectedVersion int, actor string) (policy.Policy, error) {
	saved, err := UpdateDoc(docs, PolicyKind, id, func(current policy.Policy, exists bool) (policy.Policy, error) {
		if !exists {
			return current, ErrNotFound
		}
		if expectedVersion != 0 && expectedVersion != current.Version {
			return current, fmt.Errorf("%w: policy %s is at version %d, not %d; reload and reapply your change", ErrConflict, id, current.Version, expectedVersion)
		}
		current.Name, current.Description, current.Statements = p.Name, p.Description, p.Statements
		current.Version++
		current.UpdatedAt, current.UpdatedBy = time.Now().UTC(), actorOrAnonymous(actor)
		return current, nil
	}, "iam.policy.updated", actor)
	if err != nil {
		return saved, err
	}
	return saved, recordPolicyRevision(docs, saved, actor)
}

// DeletePolicy removes a policy that is not attached anywhere. Its
// revisions are kept as history.
func DeletePolicy(docs Documents, id, actor string) error {
	if n := len(AttachmentsForPolicy(docs, id)); n > 0 {
		return fmt.Errorf("%w: policy %s is attached to %d principals; detach it first", ErrConflict, id, n)
	}
	return docs.DeleteDocument(PolicyKind, id, "iam.policy.deleted", actor)
}

func policyRevisionID(id string, version int) string { return fmt.Sprintf("%s@%d", id, version) }

// recordPolicyRevision writes the immutable snapshot of p's current
// version. A revision is never overwritten.
func recordPolicyRevision(docs Documents, p policy.Policy, actor string) error {
	_, err := UpdateDoc(docs, PolicyRevisionKind, policyRevisionID(p.ID, p.Version), func(current PolicyRevision, exists bool) (PolicyRevision, error) {
		if exists {
			return current, fmt.Errorf("%w: revision %d of policy %s already exists", ErrConflict, p.Version, p.ID)
		}
		return PolicyRevision{PolicyID: p.ID, Version: p.Version, Policy: p, Author: actorOrAnonymous(actor), CreatedAt: p.UpdatedAt}, nil
	}, "iam.policy_revision.created", actor)
	return err
}

// PolicyRevisions lists a policy's revisions, newest first.
func PolicyRevisions(docs Documents, id string) []PolicyRevision {
	out := []PolicyRevision{}
	for _, revision := range ListDocs[PolicyRevision](docs, PolicyRevisionKind) {
		if revision.PolicyID == id {
			out = append(out, revision)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out
}

// GetPolicyRevision returns one revision or ErrNotFound.
func GetPolicyRevision(docs Documents, id string, version int) (PolicyRevision, error) {
	return GetDoc[PolicyRevision](docs, PolicyRevisionKind, policyRevisionID(id, version))
}

// ---- attachments ----------------------------------------------------------------

// Attachments lists every attachment sorted by ID.
func Attachments(docs Documents) []PolicyAttachment {
	items := ListDocs[PolicyAttachment](docs, PolicyAttachmentKind)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

// AttachmentsFor lists the attachments of one principal.
func AttachmentsFor(docs Documents, principalType, principalID string) []PolicyAttachment {
	out := []PolicyAttachment{}
	for _, item := range Attachments(docs) {
		if item.PrincipalType == principalType && item.PrincipalID == principalID {
			out = append(out, item)
		}
	}
	return out
}

// AttachmentsForPolicy lists where one policy is attached.
func AttachmentsForPolicy(docs Documents, policyID string) []PolicyAttachment {
	out := []PolicyAttachment{}
	for _, item := range Attachments(docs) {
		if item.PolicyID == policyID {
			out = append(out, item)
		}
	}
	return out
}

// Attach binds a policy to a principal. Attaching the same pair twice is
// ErrConflict.
func Attach(docs Documents, policyID, principalType, principalID, actor string) (PolicyAttachment, error) {
	if principalType != PrincipalUser && principalType != PrincipalGroup {
		return PolicyAttachment{}, errors.New("principal_type must be user or group")
	}
	if strings.TrimSpace(principalID) == "" || strings.TrimSpace(policyID) == "" {
		return PolicyAttachment{}, errors.New("policy_id and principal_id are required")
	}
	id := AttachmentID(policyID, principalType, principalID)
	return UpdateDoc(docs, PolicyAttachmentKind, id, func(current PolicyAttachment, exists bool) (PolicyAttachment, error) {
		if exists {
			return current, fmt.Errorf("%w: policy %s is already attached to %s %s", ErrConflict, policyID, principalType, principalID)
		}
		return PolicyAttachment{ID: id, PolicyID: policyID, PrincipalType: principalType, PrincipalID: principalID, CreatedAt: time.Now().UTC(), CreatedBy: actorOrAnonymous(actor)}, nil
	}, "iam.policy.attached", actor)
}

// Detach removes an attachment.
func Detach(docs Documents, id, actor string) error {
	return docs.DeleteDocument(PolicyAttachmentKind, id, "iam.policy.detached", actor)
}

// AttachedBindings returns the subject's groups and every policy attached
// to the subject directly or through those groups. Attachments to deleted
// policies are skipped.
func AttachedBindings(docs Documents, subject string) ([]string, []policy.Binding) {
	groups := GroupsFor(docs, subject)
	bindings := []policy.Binding{}
	for _, attachment := range Attachments(docs) {
		var source string
		switch {
		case attachment.PrincipalType == PrincipalUser && attachment.PrincipalID == subject:
			source = "user:" + subject
		case attachment.PrincipalType == PrincipalGroup && slices.Contains(groups, attachment.PrincipalID):
			source = "group:" + attachment.PrincipalID
		default:
			continue
		}
		p, err := GetDoc[policy.Policy](docs, PolicyKind, attachment.PolicyID)
		if err != nil {
			continue
		}
		bindings = append(bindings, policy.Binding{Policy: p, Source: source})
	}
	return groups, bindings
}
