package httpapi

import (
	"io/fs"
	"net/http"
	"strings"
)

// Application pages are authored in React and exported by Next.js. The gateway
// serves page routes and navigation payloads after API and workspace handlers.
func frontendHandler(files fs.FS) http.Handler {
	assets := http.FileServer(http.FS(files))
	pages := map[string]string{"/": "index.html", "/login": "login.html", "/workspace": "workspace.html", "/api-docs": "api-docs.html", "/blog": "blog.html", "/blog/read": "blog/read.html", "/console": "console.html", "/studio": "studio.html"}
	for _, view := range []string{"projects", "pipelines", "pipeline-editor", "logs", "models", "functions", "agents", "agent-definitions", "agent-teams", "team-runs", "agent-designer", "langflow", "features", "storage", "realtime", "catalog", "platform", "access", "profile", "settings", "blogs"} {
		pages["/console/"+view] = "console/" + view + ".html"
	}
	for _, view := range []string{"posts", "drafts", "reviews", "media", "authors", "settings", "new", "edit"} {
		pages["/studio/"+view] = "studio/" + view + ".html"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := r.URL.Path
		if route != "/" && strings.HasSuffix(route, "/") {
			if _, ok := pages[strings.TrimSuffix(route, "/")]; ok {
				target := strings.TrimSuffix(route, "/")
				if r.URL.RawQuery != "" {
					target += "?" + r.URL.RawQuery
				}
				// #nosec G710 -- The path must exactly match the fixed pages allowlist above; the query cannot change its origin.
				http.Redirect(w, r, target, http.StatusPermanentRedirect)
				return
			}
		}
		if file, ok := pages[route]; ok {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			data, err := fs.ReadFile(files, file)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			if r.Method == http.MethodGet {
				_, _ = w.Write(data)
			}
			return
		}
		if strings.HasSuffix(route, ".html") || strings.HasSuffix(route, "/") {
			http.NotFound(w, r)
			return
		}
		// Next.js static exports include text component payloads for client navigation.
		// They contain build-time UI only; authenticated data remains behind the API.
		if strings.HasSuffix(route, ".txt") {
			w.Header().Set("Content-Type", "text/x-component; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
		}
		if strings.HasPrefix(route, "/_next/static/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		assets.ServeHTTP(w, r)
	})
}
