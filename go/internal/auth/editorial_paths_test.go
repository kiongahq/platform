package auth

import (
	"net/http"
	"testing"
)

// Readers must reach published blog media and the highlighter without a
// login; the editorial workspace itself must never be public.
func TestBlogReaderPathsArePublicEditorialIsNot(t *testing.T) {
	for _, path := range []string{"/media/blog/med-abcdef12/960", "/vendor/highlight/core.min.js", "/api/v1/blogs", "/blog", "/blog/read", "/_next/static/app.js", "/login.txt"} {
		if !publicPath(http.MethodGet, path) {
			t.Errorf("%s should be public", path)
		}
	}
	for _, path := range []string{"/studio", "/studio/", "/console", "/workspace", "/console/langflow", "/studio/edit", "/api/v1/editorial/posts"} {
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
	for _, path := range []string{"/fonts/ibm-plex-sans-400.ttf", "/_next/static/application.js"} {
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
