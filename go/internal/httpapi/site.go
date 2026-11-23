package httpapi

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Site settings the console needs before sign-in. KIONGA_WEBSITE_URL is the
// optional link to the Kionga website (footer and help menu); empty hides it.

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/site", s.site)
	})
	openapiPaths["/api/v1/site"] = map[string]any{"get": map[string]any{"summary": "Public install settings for the console: the optional website link and whether blog authoring is on"}}
}

// websiteURL returns KIONGA_WEBSITE_URL when it is an absolute http(s) URL
// without credentials, otherwise "".
func websiteURL() string {
	raw := strings.TrimSpace(os.Getenv("KIONGA_WEBSITE_URL"))
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	return parsed.String()
}

func (s *Server) site(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, map[string]any{"website_url": websiteURL(), "blog_authoring": blogAuthoringEnabled()})
}
