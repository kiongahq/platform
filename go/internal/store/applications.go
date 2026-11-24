package store

import (
	"time"

	"github.com/kiongahq/platform/pkg/api"
)

// ApplicationKind is the document kind that stores applications, keyed by
// name: an application name is its host label, so it is unique per install.
const ApplicationKind = "application"

// Applications lists every recorded application, newest first.
func Applications(docs Documents) []api.Application {
	return ListDocs[api.Application](docs, ApplicationKind)
}

// GetApplication returns one application or ErrNotFound.
func GetApplication(docs Documents, name string) (api.Application, error) {
	return GetDoc[api.Application](docs, ApplicationKind, name)
}

// SaveApplication creates (create=true) or replaces an application from a
// normalized request. Creating an existing name is ErrConflict; replacing a
// missing one is ErrNotFound; an application never moves between projects.
// Live status fields are kept on replace; the caller refreshes them.
func SaveApplication(docs Documents, req api.ApplicationRequest, create bool, owner, baseDomain, actor string) (api.Application, error) {
	action := "application.updated"
	if create {
		action = "application.created"
	}
	return UpdateDoc(docs, ApplicationKind, req.Name, func(current api.Application, exists bool) (api.Application, error) {
		if create && exists {
			return current, ErrConflict
		}
		if !create && !exists {
			return current, ErrNotFound
		}
		now := time.Now().UTC()
		next := current
		if !exists {
			next = api.Application{Name: req.Name, ProjectID: req.ProjectID, OwnerSubject: owner, Status: "pending", CreatedAt: now}
		} else if current.ProjectID != req.ProjectID {
			return current, ErrConflict
		}
		next.Image, next.Source, next.Port, next.Env, next.SecretRefs = req.Image, req.Source, req.Port, req.Env, req.SecretRefs
		next.Resources, next.Replicas, next.Scaling = req.Resources, *req.Replicas, *req.Scaling
		next.HealthPath, next.Visibility, next.RunAsUser = req.HealthPath, req.Visibility, req.RunAsUser
		next.URL = api.ApplicationURL(req.Name, baseDomain)
		if req.Visibility == "internal" {
			next.URL = "" // no host; reachable in the cluster only
		}
		next.UpdatedAt = now
		return next, nil
	}, action, actor)
}

// RecordApplicationStatus stores the status read back from the cluster
// without an audit record (it is bookkeeping, not a user change). It writes
// only when something changed.
func RecordApplicationStatus(docs Documents, name string, status api.Application) (api.Application, error) {
	return UpdateDoc(docs, ApplicationKind, name, func(current api.Application, exists bool) (api.Application, error) {
		if !exists {
			return current, ErrNotFound
		}
		if current.Status == status.Status && current.ReadyReplicas == status.ReadyReplicas && current.Message == status.Message && (status.URL == "" || current.URL == status.URL) {
			return current, ErrSkipWrite
		}
		current.Status, current.ReadyReplicas, current.Message = status.Status, status.ReadyReplicas, status.Message
		if status.URL != "" {
			current.URL = status.URL
		}
		return current, nil
	}, "", "")
}

// MarkApplicationRedeployed records a redeploy request.
func MarkApplicationRedeployed(docs Documents, name string, at time.Time, actor string) (api.Application, error) {
	return UpdateDoc(docs, ApplicationKind, name, func(current api.Application, exists bool) (api.Application, error) {
		if !exists {
			return current, ErrNotFound
		}
		current.RedeployedAt = &at
		current.Status = "pending"
		current.UpdatedAt = at
		return current, nil
	}, "application.redeployed", actor)
}

// DeleteApplication removes an application record.
func DeleteApplication(docs Documents, name, actor string) error {
	return docs.DeleteDocument(ApplicationKind, name, "application.deleted", actor)
}
