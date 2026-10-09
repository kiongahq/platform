package httpapi

import (
	"errors"
	"net/http"
	"os"

	"github.com/ml-ai-ops/platform/internal/policy"
	"github.com/ml-ai-ops/platform/internal/store"
)

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("PUT /api/v1/admin/users/{subject}/local-password", s.setLocalPassword)
		mux.HandleFunc("DELETE /api/v1/admin/users/{subject}/local-password", s.deleteLocalPassword)
	})
}

// LocalAccountStore adapts the document store to auth.LocalAccounts.
type LocalAccountStore struct{ Docs store.Documents }

func (l LocalAccountStore) VerifyLocalAccount(subject, password string) bool {
	return store.VerifyLocalPassword(l.Docs, subject, password)
}

func (l LocalAccountStore) LocalAccountActive(subject string) bool {
	return store.LocalAccountExists(l.Docs, subject)
}

func localAccountsEnabled() bool { return os.Getenv("OIDC_ISSUER") == "" }

// setLocalPassword lets an administrator issue a password login for a
// provisioned subject in local mode. With OIDC the identity provider owns
// credentials, so the endpoint refuses rather than creating a side door.
func (s *Server) setLocalPassword(w http.ResponseWriter, r *http.Request) {
	if !localAccountsEnabled() {
		writeError(w, http.StatusConflict, "oidc_enabled", "Passwords are managed by the identity provider when OIDC is configured.")
		return
	}
	subject := r.PathValue("subject")
	if decision := s.authorize(r, policy.UserManage, policy.UserResource(subject)); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	if _, err := s.store.AccessFor(subject); err != nil {
		writeError(w, http.StatusNotFound, "not_provisioned", "Provision this user's access before issuing a password.")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := store.SetLocalPassword(s.store, subject, body.Password, actor(r)); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_password", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subject": subject, "local_login": true})
}

func (s *Server) deleteLocalPassword(w http.ResponseWriter, r *http.Request) {
	if decision := s.authorize(r, policy.UserManage, policy.UserResource(r.PathValue("subject"))); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	err := s.store.DeleteDocument(store.LocalAccountKind, r.PathValue("subject"), "local_account.deleted", actor(r))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "This user has no local password.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
