package auth

import (
	"net/http"
	"testing"
)

// Readers must reach published blog media and the highlighter without a
// login; the editorial workspace itself must never be public.
func TestBlogReaderPathsArePublicEditorialIsNot(t *testing.T) {
	for _, path := range []string{"/media/blog/med-abcdef12/960", "/vendor/highlight/core.min.js", "/api/v1/blogs", "/blogs.html"} {
		if !publicPath(http.MethodGet, path) {
			t.Errorf("%s should be public", path)
		}
	}
	for _, path := range []string{"/editorial.html", "/editorial/app.js", "/vendor/editorjs/editorjs.umd.js", "/api/v1/editorial/posts"} {
		if publicPath(http.MethodGet, path) {
			t.Errorf("%s must require sign-in", path)
		}
	}
	if publicPath(http.MethodPost, "/media/blog/med-abcdef12/960") {
		t.Error("media writes must not be public")
	}
	if !localConsolePath("/editorial.html") || !localConsolePath("/editorial/app.js") {
		t.Error("local mode should send signed-out visitors of the workspace to login")
	}
}

func TestSharedAppearanceAssetsArePublicReadOnly(t *testing.T) {
	for _, path := range []string{"/tokens.css", "/appearance.js"} {
		if !publicPath(http.MethodGet, path) || !publicPath(http.MethodHead, path) {
			t.Errorf("%s must load on public and sign-in pages", path)
		}
		if publicPath(http.MethodPost, path) {
			t.Errorf("%s must not allow public mutations", path)
		}
	}
	if publicPath(http.MethodGet, "/js/core.js") || publicPath(http.MethodGet, "/api/v1/settings/tokens") {
		t.Error("shared appearance must not expose console code or account APIs")
	}
}
