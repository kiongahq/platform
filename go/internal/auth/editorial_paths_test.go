package auth

import (
	"net/http"
	"testing"
)

// Published blog media, the blog API (read by the separate website) and the
// highlighter stay public; the install no longer serves blog pages, and the
// editorial workspace itself must never be public.
func TestBlogReaderPathsArePublicEditorialIsNot(t *testing.T) {
	for _, path := range []string{"/media/blog/med-abcdef12/960", "/vendor/highlight/core.min.js", "/api/v1/blogs", "/api/v1/site", "/", "/_next/static/app.js", "/login.txt"} {
		if !publicPath(http.MethodGet, path) {
			t.Errorf("%s should be public", path)
		}
	}
	for _, path := range []string{"/blog", "/blog/read", "/studio", "/studio/", "/console", "/console/applications", "/workspace", "/console/langflow", "/studio/edit", "/api/v1/editorial/posts"} {
		if publicPath(http.MethodGet, path) {
			t.Errorf("%s must require sign-in", path)
		}
	}
	if publicPath(http.MethodPost, "/media/blog/med-abcdef12/960") {
		t.Error("media writes must not be public")
	}
	if !localConsolePath("/studio") || !localConsolePath("/studio/edit") {
		t.Error("local mode should send signed-out visitors of the workspace to login")
	}
}

func TestFrontendFontAndFrameworkAssetsArePublicReadOnly(t *testing.T) {
	for _, path := range []string{"/fonts/ibm-plex-sans-400.ttf", "/_next/static/application.js", "/favicon.svg", "/favicon-cobalt.svg"} {
		if !publicPath(http.MethodGet, path) || !publicPath(http.MethodHead, path) {
			t.Errorf("%s must load on public and sign-in pages", path)
		}
		if publicPath(http.MethodPost, path) {
			t.Errorf("%s must not allow public mutations", path)
		}
	}
	if publicPath(http.MethodGet, "/js/core.js") || publicPath(http.MethodGet, "/api/v1/settings/tokens") {
		t.Error("public frontend assets must not expose account APIs")
	}
}
