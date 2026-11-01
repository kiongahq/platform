package httpapi

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/store"
)

var githubHTTP = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}

const githubAccountKind = "github_account"

type githubAccount struct {
	Login          string    `json:"login"`
	Ciphertext     string    `json:"ciphertext"`
	InstallationID int64     `json:"installation_id,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitzero"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type githubInstallation struct {
	ID                  int64  `json:"id"`
	AppSlug             string `json:"app_slug"`
	RepositorySelection string `json:"repository_selection"`
	Account             struct {
		Login string `json:"login"`
	} `json:"account"`
}
type githubRepository struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
}

func githubAccountID(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:])
}
func (s *Server) readGitHubAccount(subject string) (githubAccount, error) {
	return store.GetDoc[githubAccount](s.store, githubAccountKind, githubAccountID(subject))
}
func (s *Server) saveGitHubAccount(subject string, account githubAccount) error {
	account.UpdatedAt = time.Now().UTC()
	_, err := store.UpdateDoc(s.store, githubAccountKind, githubAccountID(subject), func(_ githubAccount, _ bool) (githubAccount, error) { return account, nil }, "github.account.updated", subject)
	return err
}
func openGitHubSecret(subject, encrypted string) (string, error) {
	aead, err := hubCipher()
	if err != nil {
		return "", errors.New("Configure KIONGA_CREDENTIAL_KEY to encrypt GitHub account connections")
	}
	data, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil || len(data) < aead.NonceSize() {
		return "", errors.New("Reconnect your GitHub account")
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(subject))
	if err != nil {
		return "", errors.New("Reconnect your GitHub account")
	}
	return string(plain), nil
}
func (s *Server) githubToken(subject string) (githubAccount, string, error) {
	account, err := s.readGitHubAccount(subject)
	if err != nil || account.Ciphertext == "" {
		return account, "", errors.New("Connect your GitHub account first")
	}
	if !account.ExpiresAt.IsZero() && time.Now().After(account.ExpiresAt) {
		return account, "", errors.New("Your GitHub authorization expired. Reconnect your account")
	}
	token, err := openGitHubSecret("github:"+subject, account.Ciphertext)
	return account, token, err
}
func githubRequest(r *http.Request, path, token string, result any) error {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://api.github.com"+path, nil)
	if err != nil {
		return errors.New("Invalid GitHub request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := githubHTTP.Do(req)
	if err != nil {
		return errors.New("GitHub could not be reached. Please retry")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub returned HTTP %d. Check your account authorization and repository permissions", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(result); err != nil {
		return errors.New("GitHub returned an invalid response")
	}
	return nil
}

var githubSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func githubAppSlug() string {
	slug := os.Getenv("KIONGA_GITHUB_APP_SLUG")
	if !githubSlugPattern.MatchString(slug) {
		return ""
	}
	return slug
}
func githubCallback() string {
	raw := os.Getenv("KIONGA_GITHUB_APP_CALLBACK_URL")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Path != "/api/v1/settings/github/callback" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	return raw
}
func githubOAuthConfigured() bool {
	return githubAppSlug() != "" && os.Getenv("KIONGA_GITHUB_APP_CLIENT_ID") != "" && os.Getenv("KIONGA_GITHUB_APP_CLIENT_SECRET") != "" && githubCallback() != ""
}
func (s *Server) githubAccount(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	subject := principal(r).Subject
	switch r.Method {
	case http.MethodPut:
		var body struct {
			Token          string `json:"token"`
			InstallationID int64  `json:"installation_id"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if decode(r, &body) != nil {
			writeError(w, 400, "invalid_request", "Enter a token or choose an installation")
			return
		}
		if body.Token != "" {
			body.Token = strings.TrimSpace(body.Token)
			if len(body.Token) > 2048 || strings.ContainsAny(body.Token, "\r\n") {
				writeError(w, 400, "invalid_token", "Enter a GitHub access token")
				return
			}
			cipher, err := sealHubToken("github:"+subject, body.Token)
			if err != nil {
				writeError(w, 503, "not_configured", "Configure KIONGA_CREDENTIAL_KEY to encrypt GitHub credentials")
				return
			}
			var identity struct {
				Login string `json:"login"`
			}
			if err := githubRequest(r, "/user", body.Token, &identity); err != nil || identity.Login == "" {
				writeError(w, 422, "github_error", "GitHub did not accept this account token")
				return
			}
			if err := s.saveGitHubAccount(subject, githubAccount{Login: identity.Login, Ciphertext: cipher}); err != nil {
				writeError(w, 500, "storage_error", "Could not save GitHub connection")
				return
			}
		} else if body.InstallationID > 0 {
			account, token, err := s.githubToken(subject)
			if err != nil {
				writeError(w, 422, "github_error", err.Error())
				return
			}
			installations, err := githubInstallations(r, token)
			if err != nil {
				writeError(w, 422, "github_error", err.Error())
				return
			}
			found := false
			for _, installation := range installations {
				if installation.ID == body.InstallationID {
					found = true
				}
			}
			if !found {
				writeError(w, 403, "access_denied", "This installation is not accessible to your GitHub account for the configured app")
				return
			}
			account.InstallationID = body.InstallationID
			if err := s.saveGitHubAccount(subject, account); err != nil {
				writeError(w, 500, "storage_error", "Could not save installation")
				return
			}
		} else {
			writeError(w, 400, "invalid_request", "Enter a token or choose an installation")
			return
		}
	case http.MethodDelete:
		if err := s.store.DeleteDocument(githubAccountKind, githubAccountID(subject), "github.account.disconnected", subject); err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, 500, "storage_error", "Could not disconnect GitHub account")
			return
		}
	}
	account, _ := s.readGitHubAccount(subject)
	_, keyErr := hubCipher()
	installURL := ""
	if slug := githubAppSlug(); slug != "" {
		installURL = "https://github.com/apps/" + slug + "/installations/new"
	}
	writeJSON(w, 200, map[string]any{"connected": account.Ciphertext != "" && (account.ExpiresAt.IsZero() || time.Now().Before(account.ExpiresAt)), "configured": keyErr == nil, "username": account.Login, "installation_id": account.InstallationID, "updated_at": account.UpdatedAt, "expires_at": account.ExpiresAt, "oauth_configured": githubOAuthConfigured(), "install_url": installURL})
}
func githubInstallations(r *http.Request, token string) ([]githubInstallation, error) {
	if githubAppSlug() == "" {
		return nil, errors.New("A GitHub App is not configured")
	}
	out := []githubInstallation{}
	for page := 1; page <= 10; page++ {
		var result struct {
			Installations []githubInstallation `json:"installations"`
		}
		if err := githubRequest(r, fmt.Sprintf("/user/installations?per_page=100&page=%d", page), token, &result); err != nil {
			return nil, err
		}
		for _, installation := range result.Installations {
			if installation.AppSlug == githubAppSlug() {
				out = append(out, installation)
			}
		}
		if len(result.Installations) < 100 {
			return out, nil
		}
	}
	return nil, errors.New("Too many GitHub installations; narrow the account's app access")
}
func (s *Server) listGitHubInstallations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	_, token, err := s.githubToken(principal(r).Subject)
	if err != nil {
		writeError(w, 422, "github_error", err.Error())
		return
	}
	items, err := githubInstallations(r, token)
	if err != nil {
		writeError(w, 422, "github_error", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) githubRepositories(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	account, token, err := s.githubToken(principal(r).Subject)
	if err != nil {
		writeError(w, 422, "github_error", err.Error())
		return
	}
	out := []githubRepository{}
	for page := 1; page <= 10; page++ {
		var batch []githubRepository
		if account.InstallationID > 0 {
			var result struct {
				Repositories []githubRepository `json:"repositories"`
			}
			err = githubRequest(r, fmt.Sprintf("/user/installations/%d/repositories?per_page=100&page=%d", account.InstallationID, page), token, &result)
			batch = result.Repositories
		} else {
			err = githubRequest(r, fmt.Sprintf("/user/repos?per_page=100&page=%d&sort=updated&affiliation=owner,collaborator,organization_member", page), token, &batch)
		}
		if err != nil {
			writeError(w, 422, "github_error", err.Error())
			return
		}
		for _, repository := range batch {
			if !repository.Archived {
				out = append(out, repository)
			}
		}
		if len(batch) < 100 {
			writeJSON(w, 200, map[string]any{"items": out, "installation_id": account.InstallationID})
			return
		}
	}
	writeError(w, 422, "too_many_repositories", "More than 1,000 repositories are available. Limit the GitHub App installation to selected repositories")
}
func (s *Server) githubConnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	_, keyErr := hubCipher()
	if !githubOAuthConfigured() || keyErr != nil {
		writeError(w, 503, "not_configured", "Configure the GitHub App client ID, client secret, app slug, callback URL and credential key first")
		return
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		writeError(w, 500, "oauth_error", "Could not start GitHub authorization")
		return
	}
	state := hex.EncodeToString(nonce)
	value := state + ":" + strconv.FormatInt(time.Now().Unix(), 10)
	cipher, err := sealHubToken("github-oauth:"+principal(r).Subject, value)
	if err != nil {
		writeError(w, 503, "not_configured", "GitHub account encryption is unavailable")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "kionga_github_oauth", Value: cipher, Path: "/api/v1/settings/github", HttpOnly: true, Secure: strings.HasPrefix(githubCallback(), "https:"), SameSite: http.SameSiteLaxMode, MaxAge: 600})
	q := url.Values{"client_id": {os.Getenv("KIONGA_GITHUB_APP_CLIENT_ID")}, "redirect_uri": {githubCallback()}, "state": {state}}
	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+q.Encode(), http.StatusFound)
}
func (s *Server) githubCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	subject := principal(r).Subject
	cookie, err := r.Cookie("kionga_github_oauth")
	if err != nil {
		writeError(w, 400, "invalid_state", "Restart GitHub authorization from Settings")
		return
	}
	value, err := openGitHubSecret("github-oauth:"+subject, cookie.Value)
	parts := strings.Split(value, ":")
	valid := err == nil && len(parts) == 2
	if valid {
		at, parseErr := strconv.ParseInt(parts[1], 10, 64)
		age := time.Now().Unix() - at
		valid = parseErr == nil && age >= 0 && age <= 600 && subtle.ConstantTimeCompare([]byte(parts[0]), []byte(r.URL.Query().Get("state"))) == 1
	}
	http.SetCookie(w, &http.Cookie{Name: "kionga_github_oauth", Value: "", Path: "/api/v1/settings/github", HttpOnly: true, Secure: strings.HasPrefix(githubCallback(), "https:"), SameSite: http.SameSiteLaxMode, MaxAge: -1})
	if !valid || !githubOAuthConfigured() {
		writeError(w, 400, "invalid_state", "GitHub authorization expired or belongs to another session")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 512 {
		http.Redirect(w, r, "/console/settings?github=cancelled", http.StatusSeeOther)
		return
	}
	payload, _ := json.Marshal(map[string]string{"client_id": os.Getenv("KIONGA_GITHUB_APP_CLIENT_ID"), "client_secret": os.Getenv("KIONGA_GITHUB_APP_CLIENT_SECRET"), "code": code, "redirect_uri": githubCallback()})
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://github.com/login/oauth/access_token", bytes.NewReader(payload))
	if err != nil {
		writeError(w, 500, "oauth_error", "Could not exchange GitHub authorization")
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := githubHTTP.Do(request)
	if err != nil {
		writeError(w, 502, "oauth_error", "GitHub authorization could not be completed")
		return
	}
	defer response.Body.Close()
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&token) != nil || token.AccessToken == "" {
		writeError(w, 422, "oauth_error", "GitHub did not authorize this account. Restart authorization")
		return
	}
	var identity struct {
		Login string `json:"login"`
	}
	if err := githubRequest(r, "/user", token.AccessToken, &identity); err != nil || identity.Login == "" {
		writeError(w, 422, "github_error", "Could not verify the authorized GitHub account")
		return
	}
	cipher, err := sealHubToken("github:"+subject, token.AccessToken)
	if err != nil {
		writeError(w, 503, "not_configured", "GitHub credential encryption is unavailable")
		return
	}
	account := githubAccount{Login: identity.Login, Ciphertext: cipher}
	if token.ExpiresIn > 0 {
		account.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	if err := s.saveGitHubAccount(subject, account); err != nil {
		writeError(w, 500, "storage_error", "Could not save GitHub authorization")
		return
	}
	http.Redirect(w, r, "/console/settings?github=connected", http.StatusSeeOther)
}

func init() {
	openapiPaths["/api/v1/settings/github"] = map[string]any{"get": map[string]any{"summary": "Personal GitHub connection status; never returns credentials"}, "put": map[string]any{"summary": "Connect an encrypted personal token or verified GitHub App installation"}, "delete": map[string]any{"summary": "Disconnect the personal GitHub account"}}
	openapiPaths["/api/v1/settings/github/repositories"] = map[string]any{"get": map[string]any{"summary": "List repositories accessible to the connected user and selected app installation"}}
	openapiPaths["/api/v1/settings/github/installations"] = map[string]any{"get": map[string]any{"summary": "List configured-app installations accessible to the connected GitHub user"}}
}
