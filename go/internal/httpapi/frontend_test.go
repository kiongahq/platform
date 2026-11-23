package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestFrontendRoutes(t *testing.T) {
	files := fstest.MapFS{"index.html": {Data: []byte("home")}, "studio.html": {Data: []byte("studio")}, "console/langflow.html": {Data: []byte("Langflow")}, "console/langflow.txt": {Data: []byte("component payload")}, "studio/edit.html": {Data: []byte("editor")}, "_next/static/app.js": {Data: []byte("code")}}
	handler := frontendHandler(files)
	for _, tc := range []struct {
		path     string
		status   int
		location string
	}{
		{"/", 302, "/console"}, {"/blog", 404, ""}, {"/blog/read?slug=first", 404, ""}, {"/studio", 200, ""},
		{"/console/langflow", 200, ""}, {"/console/langflow.txt", 200, ""}, {"/studio/edit?post=first", 200, ""},
		{"/blogs.html?tag=Go", 404, ""}, {"/blog.html?slug=first", 404, ""},
		{"/editorial.html", 404, ""}, {"/blog/", 404, ""}, {"/studio/", 308, "/studio"},
		{"/console/langflow/?return_to=https%3A%2F%2Fevil.test", 308, "/console/langflow?return_to=https%3A%2F%2Fevil.test"},
		{"//evil.test/", 404, ""}, {"/%2f%2fevil.test/", 404, ""},
		{"https://evil.test/studio/", 308, "/studio"},
		{"/missing", 404, ""}, {"/blog/read.html", 404, ""}, {"/_next/static/app.js", 200, ""}, {"/_next/static/", 404, ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, newTestRequest(http.MethodGet, tc.path, nil))
			if w.Code != tc.status || w.Header().Get("Location") != tc.location {
				t.Fatalf("got %d %s", w.Code, w.Header().Get("Location"))
			}
		})
	}
}
