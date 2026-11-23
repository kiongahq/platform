package httpapi

import (
	"net/http"
	"os"
	"strings"
)

// Blog authoring (the writing studio, the console Publishing page, the
// editorial API, the public article API and blog media) is an opt-in
// feature: KIONGA_BLOG_AUTHORING_ENABLED=true turns it on. Off by default,
// every one of those routes answers 404 and the console hides the
// navigation, so an install carries no publishing surface it does not use.

func blogAuthoringEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KIONGA_BLOG_AUTHORING_ENABLED"))) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// blogPath reports whether a request path belongs to blog authoring.
func blogPath(path string) bool {
	for _, prefix := range []string{"/api/v1/editorial", "/api/v1/blogs", "/media/blog/", "/studio", "/console/blogs"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") || strings.HasPrefix(path, prefix+".") || (strings.HasSuffix(prefix, "/") && strings.HasPrefix(path, prefix)) {
			return true
		}
	}
	return false
}

// blogGate answers 404 for blog routes while the feature is off. It runs
// before sign-in and roles, so a disabled feature looks absent to everyone.
func blogGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !blogAuthoringEnabled() && blogPath(r.URL.Path) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeError(w, http.StatusNotFound, "not_found", "blog authoring is not enabled on this install")
				return
			}
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
