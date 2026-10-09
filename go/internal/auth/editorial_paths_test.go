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
