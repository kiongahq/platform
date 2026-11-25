package httpapi

import (
	"net/http"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// pipelineRunView is a run as the list and detail endpoints return it: the
// stored run plus owner_label, a display name for owner_subject resolved at
// read time. The label is never stored.
type pipelineRunView struct {
	api.PipelineRun
	OwnerLabel string `json:"owner_label,omitempty"`
}

// userDirectoryPath is the endpoint that lists every person's access profile,
// email included. Whoever may read it may see any owner's email.
const userDirectoryPath = "/api/v1/admin/users"

// ownerLabels resolves owner_subject to a display label for one caller
// without widening who can see an email:
//
//   - a caller who may read the user directory (GET /api/v1/admin/users:
//     administrators and operators) gets the email on the owner's access
//     profile;
//   - the owner gets their own email;
//   - everyone else, project co-members included, gets owner_subject
//     unchanged, because no endpoint they can call maps a subject to an email.
//
// An owner with no access profile, or a profile without an email, is labelled
// with owner_subject.
type ownerLabels struct {
	store     store.Repository
	caller    auth.Principal
	directory bool
	emails    map[string]string
}

func (s *Server) ownerLabelsFor(r *http.Request) *ownerLabels {
	caller := principal(r)
	return &ownerLabels{store: s.store, caller: caller, directory: auth.Allowed(caller, http.MethodGet, userDirectoryPath)}
}

// profileEmail returns the email on a subject's access profile, or "".
func (o *ownerLabels) profileEmail(subject string) string {
	if o.emails == nil {
		o.emails = map[string]string{}
	}
	email, known := o.emails[subject]
	if !known {
		if access, err := o.store.AccessFor(subject); err == nil {
			email = access.Email
		}
		o.emails[subject] = email
	}
	return email
}

func (o *ownerLabels) label(owner string) string {
	if owner == "" {
		return ""
	}
	// owner_subject holds the submitter's subject, or their token email when
	// the token carries one, so the caller is the owner on either match.
	if o.caller.Subject != "" && (owner == o.caller.Subject || (o.caller.Email != "" && owner == o.caller.Email)) {
		if o.caller.Email != "" {
			return o.caller.Email
		}
		if email := o.profileEmail(o.caller.Subject); email != "" {
			return email
		}
		return owner
	}
	if o.directory {
		if email := o.profileEmail(owner); email != "" {
			return email
		}
	}
	return owner
}

func (o *ownerLabels) run(run api.PipelineRun) pipelineRunView {
	return pipelineRunView{PipelineRun: run, OwnerLabel: o.label(run.OwnerSubject)}
}

func (o *ownerLabels) runs(items []api.PipelineRun) []pipelineRunView {
	out := make([]pipelineRunView, 0, len(items))
	for _, item := range items {
		out = append(out, o.run(item))
	}
	return out
}
