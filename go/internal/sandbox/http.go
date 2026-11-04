package sandbox

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/sandbox/client"
)

const maxJSONBody = 1 << 20

// NewHandler serves the contract. Only /healthz is open, and it carries no secret.
// Logs hold the route and status only: request bodies carry credentials and paths.
func NewHandler(service *Service, token string, logger *log.Logger) http.Handler {
	api := &api{service: service, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.health)
	mux.Handle("POST /sandboxes", authorize(token, api.create))
	mux.Handle("POST /sandboxes/{id}/exec", authorize(token, api.exec))
	mux.Handle("GET /sandboxes/{id}/files", authorize(token, api.readFile))
	mux.Handle("PUT /sandboxes/{id}/files", authorize(token, api.writeFile))
	mux.Handle("POST /sandboxes/{id}/git/clone", authorize(token, api.gitClone))
	mux.Handle("POST /sandboxes/{id}/git/push", authorize(token, api.gitPush))
	mux.Handle("GET /sandboxes/{id}/git/diff", authorize(token, api.gitDiff))
	mux.Handle("DELETE /sandboxes/{id}", authorize(token, api.remove))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		started := time.Now()
		mux.ServeHTTP(recorder, r)
		if r.URL.Path != "/healthz" {
			pattern := r.Pattern
			if pattern == "" {
				pattern = r.Method + " (no route)"
			}
			logger.Printf("sandbox: %s -> %d in %s", pattern, recorder.status, time.Since(started).Round(time.Millisecond))
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// authorize compares in constant time and fails closed on an empty configured token.
func authorize(token string, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || !ok || subtle.ConstantTimeCompare([]byte(bearer), []byte(token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, client.Error{Code: client.CodeUnauthorized, Message: "a valid bearer token is required"})
			return
		}
		next(w, r)
	})
}

type api struct {
	service *Service
	logger  *log.Logger
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (a *api) fail(w http.ResponseWriter, err error) {
	var failure *client.Error
	if !errors.As(err, &failure) {
		// Engine text can describe the host, so it stays in the log. Callers get a code.
		a.logger.Printf("sandbox: internal error: %v", err)
		failure = &client.Error{Status: http.StatusInternalServerError, Code: client.CodeInternal, Message: "the sandbox manager failed"}
	}
	message := failure.Message
	if runes := []rune(message); len(runes) > 500 {
		message = string(runes[:500])
	}
	writeJSON(w, failure.Status, client.Error{Code: failure.Code, Message: message})
}

// decode is strict: the contract is closed, so an unknown field is a caller bug, and
// a body field like "env" or "privileged" must fail rather than be quietly ignored.
func decode(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return invalid("the request body is not valid for this operation")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return invalid("the request body has trailing data")
	}
	return nil
}

func (a *api) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.service.Health(r.Context()))
}

func (a *api) create(w http.ResponseWriter, r *http.Request) {
	var request client.CreateRequest
	if err := decode(w, r, &request); err != nil {
		a.fail(w, err)
		return
	}
	sandbox, err := a.service.Create(r.Context(), request)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sandbox)
}

func (a *api) exec(w http.ResponseWriter, r *http.Request) {
	var request client.ExecRequest
	if err := decode(w, r, &request); err != nil {
		a.fail(w, err)
		return
	}
	result, err := a.service.Exec(r.Context(), r.PathValue("id"), request)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *api) readFile(w http.ResponseWriter, r *http.Request) {
	content, err := a.service.ReadPath(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, content)
}

func (a *api) writeFile(w http.ResponseWriter, r *http.Request) {
	content, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFileWrite))
	if err != nil {
		a.fail(w, &client.Error{Status: http.StatusRequestEntityTooLarge, Code: client.CodeTooLarge, Message: "the file exceeds the write limit"})
		return
	}
	result, err := a.service.WriteFile(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"), content)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *api) gitClone(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Repository string `json:"repository"`
		Ref        string `json:"ref"`
		Credential string `json:"credential"`
	}
	if err := decode(w, r, &request); err != nil {
		a.fail(w, err)
		return
	}
	result, err := a.service.GitClone(r.Context(), r.PathValue("id"), client.GitCloneRequest{Repository: request.Repository, Ref: request.Ref, Credential: client.Credential(request.Credential)})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *api) gitPush(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Repository string `json:"repository"`
		Branch     string `json:"branch"`
		Credential string `json:"credential"`
	}
	if err := decode(w, r, &request); err != nil {
		a.fail(w, err)
		return
	}
	result, err := a.service.GitPush(r.Context(), r.PathValue("id"), client.GitPushRequest{Repository: request.Repository, Branch: request.Branch, Credential: client.Credential(request.Credential)})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *api) gitDiff(w http.ResponseWriter, r *http.Request) {
	result, err := a.service.GitDiff(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *api) remove(w http.ResponseWriter, r *http.Request) {
	if err := a.service.Delete(r.Context(), r.PathValue("id")); err != nil {
		if client.Code(err) != client.CodeNotFound {
			a.fail(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
