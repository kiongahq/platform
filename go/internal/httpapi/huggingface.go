package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

var hubHTTP = &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
var hubRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}(/[A-Za-z0-9][A-Za-z0-9._-]{0,95})?$`)
var hubCommit = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

type hubModel struct {
	ID          string   `json:"id"`
	SHA         string   `json:"sha"`
	PipelineTag string   `json:"pipeline_tag"`
	LibraryName string   `json:"library_name"`
	Downloads   int64    `json:"downloads"`
	Likes       int64    `json:"likes"`
	Private     bool     `json:"private"`
	Gated       any      `json:"gated"`
	Tags        []string `json:"tags"`
}

func sealHubToken(ctx context.Context, subject, token string) (string, error) {
	return sealCredential(ctx, hubCredentialAAD(subject), token)
}
func (s *Server) hubToken(ctx context.Context, subject string) (string, error) {
	account, err := s.store.HubAccount(subject)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("Could not read your Hugging Face account")
	}
	if account.Ciphertext == "" {
		return "", nil
	}
	plain, err := openCredential(ctx, hubCredentialAAD(subject), account.Ciphertext)
	if errors.Is(err, errCredentialUnreadable) {
		return "", errors.New("Account credential cannot be decrypted; reconnect your account")
	}
	return plain, err
}

// The host is fixed: a browser cannot direct its saved credential to another host.
func hubRequest(r *http.Request, path, token string, result any) error {
	req, err := http.NewRequestWithContext(r.Context(), "GET", "https://huggingface.co"+path, nil)
	if err != nil {
		return errors.New("Invalid Hub request")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hubHTTP.Do(req)
	if err != nil {
		return errors.New("Hugging Face could not be reached. Please retry")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
	case 401, 403:
		return errors.New("Hugging Face denied access. Check your token and accept any gated model terms on huggingface.co")
	case 404:
		return errors.New("Model or revision not found, or your account cannot access it")
	case 429:
		return errors.New("Hugging Face rate limit reached. Please retry later")
	default:
		return fmt.Errorf("Hugging Face returned HTTP %d. Please retry later", resp.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(result); err != nil {
		return errors.New("Invalid response from Hugging Face")
	}
	return nil
}

func (s *Server) huggingFaceAccount(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	w.Header().Set("Cache-Control", "no-store")
	subject := principal(r).Subject
	switch r.Method {
	case "PUT":
		var req struct {
			Token string `json:"token"`
		}
		if err := requireJSON(r); err != nil {
			writeDecodeError(w, err)
			return
		}
		if decode(r, &req) != nil || !strings.HasPrefix(req.Token, "hf_") || len(req.Token) > 1024 {
			writeError(w, 400, "invalid_token", "Enter a Hugging Face read or fine-grained token")
			return
		}
		encrypted, err := sealHubToken(r.Context(), subject, req.Token)
		if err != nil {
			writeError(w, 503, "not_configured", err.Error())
			return
		}
		var identity struct {
			Name string `json:"name"`
		}
		if err = hubRequest(r, "/api/whoami-v2", req.Token, &identity); err != nil {
			writeError(w, 422, "hub_error", err.Error())
			return
		}
		if identity.Name == "" {
			writeError(w, 422, "hub_error", "Hugging Face did not return an account name")
			return
		}
		if err = s.store.SaveHubAccount(subject, store.HubAccount{Username: identity.Name, Ciphertext: encrypted}); err != nil {
			writeError(w, 500, "storage_error", "Could not save account")
			return
		}
	case "DELETE":
		if err := s.store.SaveHubAccount(subject, store.HubAccount{}); err != nil {
			writeError(w, 500, "storage_error", "Could not disconnect account")
			return
		}
	}
	account, err := s.store.HubAccount(subject)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, "storage_error", "Could not read account")
		return
	}
	writeJSON(w, 200, map[string]any{"connected": account.Ciphertext != "", "username": account.Username, "configured": credentialsConfigured()})
}

func (s *Server) huggingFaceModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, err := s.hubToken(r.Context(), principal(r).Subject)
	if err != nil {
		writeError(w, 503, "credential_unavailable", err.Error())
		return
	}
	q := url.Values{"limit": {"20"}, "sort": {"downloads"}, "direction": {"-1"}, "full": {"true"}}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	if len(search) > 200 {
		writeError(w, 400, "invalid_search", "Search must be 200 characters or fewer")
		return
	}
	q.Set("search", search)
	if task := r.URL.Query().Get("task"); task != "" {
		q.Set("pipeline_tag", task)
	}
	var models []hubModel
	if err = hubRequest(r, "/api/models?"+q.Encode(), token, &models); err != nil {
		writeError(w, 502, "hub_error", err.Error())
		return
	}
	if models == nil {
		models = []hubModel{}
	}
	writeJSON(w, 200, map[string]any{"items": models})
}

func (s *Server) importHuggingFaceModel(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req struct {
		ProjectID string `json:"project_id"`
		RepoID    string `json:"repo_id"`
		Revision  string `json:"revision"`
	}
	if err := requireJSON(r); err != nil {
		writeDecodeError(w, err)
		return
	}
	if decode(r, &req) != nil || !hubRepo.MatchString(req.RepoID) || strings.Contains(req.RepoID, "..") || len(req.Revision) > 200 {
		writeError(w, 400, "invalid_request", "Provide a project, Hugging Face repository ID and valid revision")
		return
	}
	if !projectAllowed(s.store, principal(r), req.ProjectID) {
		writeError(w, 403, "forbidden", "This project has not been assigned to you")
		return
	}
	if _, err := s.store.Project(req.ProjectID); err != nil {
		writeError(w, 404, "not_found", "Project not found")
		return
	}
	if req.Revision == "" {
		req.Revision = "main"
	}
	token, err := s.hubToken(r.Context(), principal(r).Subject)
	if err != nil {
		writeError(w, 503, "credential_unavailable", err.Error())
		return
	}
	var info hubModel
	if err = hubRequest(r, "/api/models/"+req.RepoID+"/revision/"+url.PathEscape(req.Revision), token, &info); err != nil {
		writeError(w, 422, "hub_error", err.Error())
		return
	}
	if !hubCommit.MatchString(info.SHA) {
		writeError(w, 502, "hub_error", "Hugging Face did not provide an immutable commit")
		return
	}
	// Register provenance only, never execute remote code or download weights on the gateway.
	source := &api.ModelSource{Provider: "huggingface", Repository: req.RepoID, Revision: info.SHA, Task: info.PipelineTag, Library: info.LibraryName}
	for _, tag := range info.Tags {
		if strings.HasPrefix(tag, "license:") {
			source.License = strings.TrimPrefix(tag, "license:")
			break
		}
	}
	model, err := s.store.RegisterModel(api.RegisterModelRequest{ProjectID: req.ProjectID, Name: req.RepoID, Version: info.SHA, ArtifactURI: "hf://" + req.RepoID + "@" + info.SHA, Metrics: map[string]float64{}, Source: source}, actor(r))
	writeMutation(w, model, err, http.StatusCreated)
}
