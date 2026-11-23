package httpapi

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

func TestSeedEngineeringBlogIsPublic(t *testing.T) {
	t.Setenv("KIONGA_BLOG_AUTHORING_ENABLED", "true")
	t.Setenv("KIONGA_BLOG_MEDIA_DIR", t.TempDir())
	server := testServer()
	list := httptest.NewRecorder()
	server.ServeHTTP(list, newTestRequest(http.MethodGet, "/api/v1/blogs", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "mounting-s3-as-a-filesystem-in-jupyter") || strings.Contains(list.Body.String(), "rendered_html") {
		t.Fatalf("blog list: %d %s", list.Code, list.Body.String())
	}
	post := httptest.NewRecorder()
	server.ServeHTTP(post, newTestRequest(http.MethodGet, "/api/v1/blogs/mounting-s3-as-a-filesystem-in-jupyter", nil))
	var body api.PublicBlogPost
	_ = json.Unmarshal(post.Body.Bytes(), &body)
	if post.Code != http.StatusOK || !strings.Contains(body.RenderedHTML, `id="production-checklist"`) || len(body.Blocks) < 20 || !strings.Contains(body.Content, "## Production checklist") || body.Author != "Kionga Engineering" || body.PublishedAt == nil {
		t.Fatalf("blog post: %d %s", post.Code, post.Body.String()[:min(400, post.Body.Len())])
	}
	if !strings.Contains(body.RenderedHTML, `<pre class="kb-code"><code class="language-dockerfile">`) || !strings.Contains(body.RenderedHTML, "kb-checklist") {
		t.Fatal("migrated blocks lost code languages or checklists")
	}
}

func TestLegacyAdminBlogEndpointsAreGone(t *testing.T) {
	t.Setenv("KIONGA_BLOG_AUTHORING_ENABLED", "true")
	t.Setenv("KIONGA_BLOG_MEDIA_DIR", t.TempDir())
	server := testServer()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, newTestRequest(method, "/api/v1/admin/blogs", strings.NewReader(`{}`)))
		if response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s legacy endpoint still served: %d", method, response.Code)
		}
	}
}

// editorialClient drives the API as one platform identity with its own
// editorial cookie jar.
type editorialClient struct {
	t       *testing.T
	server  http.Handler
	subject string
	roles   []string
	cookie  *http.Cookie
}

func (c *editorialClient) do(method, path string, body any, contentType ...string) *httptest.ResponseRecorder {
	c.t.Helper()
	var reader io.Reader
	switch value := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(value)
	default:
		raw, _ := json.Marshal(value)
		reader = bytes.NewReader(raw)
	}
	request := newTestRequest(method, path, reader)
	if len(contentType) > 0 {
		request.Header.Set("Content-Type", contentType[0])
	}
	if c.subject != "" {
		request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: c.subject, Roles: c.roles}))
	}
	if c.cookie != nil {
		request.AddCookie(c.cookie)
	}
	response := httptest.NewRecorder()
	c.server.ServeHTTP(response, request)
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == EditorialCookie {
			c.cookie = cookie
		}
	}
	return response
}

func decodeInto[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %d %s: %v", response.Code, response.Body.String(), err)
	}
	return value
}

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), 90, uint8(y), 255})
		}
	}
	var buffer bytes.Buffer
	_ = jpeg.Encode(&buffer, img, nil)
	return buffer.Bytes()
}

func multipartImage(t *testing.T, data []byte, alt string) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "photo.png") // the filename lies; sniffing decides
	_, _ = part.Write(data)
	_ = writer.WriteField("alt", alt)
	_ = writer.Close()
	return body.Bytes(), writer.FormDataContentType()
}

func paragraphs(words int) []api.Block {
	raw, _ := json.Marshal(map[string]string{"text": strings.Repeat("practical words ", words/2)})
	return []api.Block{{ID: "p1", Type: "paragraph", Data: raw}}
}

func TestEditorialSessionSeparationAndWorkflow(t *testing.T) {
	t.Setenv("KIONGA_BLOG_AUTHORING_ENABLED", "true")
	t.Setenv("KIONGA_BLOG_MEDIA_DIR", t.TempDir())
	t.Setenv("KIONGA_LOCAL_USERNAME", "admin")
	server := testServer()
	mlAdmin := &editorialClient{t: t, server: server, subject: "ops-admin", roles: []string{auth.RoleAdmin}}
	bootstrapAdmin := &editorialClient{t: t, server: server, subject: "admin", roles: []string{auth.RoleAdmin}}
	ana := &editorialClient{t: t, server: server, subject: "ana", roles: []string{auth.RoleUser}}
	eve := &editorialClient{t: t, server: server, subject: "eve", roles: []string{auth.RoleViewer}}
	anonymous := &editorialClient{t: t, server: server}

	// An ML administrator without membership gets no editorial access.
	if response := mlAdmin.do(http.MethodPost, "/api/v1/editorial/session", nil); response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"can_bootstrap":true`) {
		t.Fatalf("ML admin session: %d %s", response.Code, response.Body.String())
	}
	for _, path := range []string{"/api/v1/editorial/posts", "/api/v1/editorial/members", "/api/v1/editorial/media"} {
		if response := mlAdmin.do(http.MethodGet, path, nil); response.Code != http.StatusForbidden {
			t.Fatalf("ML admin reached %s: %d", path, response.Code)
		}
	}
	if response := ana.do(http.MethodPost, "/api/v1/editorial/bootstrap", nil); response.Code != http.StatusForbidden {
		t.Fatalf("normal user bootstrapped: %d", response.Code)
	}

	// Bootstrap mints the first editorial admin and a strict cookie.
	response := bootstrapAdmin.do(http.MethodPost, "/api/v1/editorial/bootstrap", map[string]string{"display_name": "Platform Admin"})
	if response.Code != http.StatusCreated {
		t.Fatalf("bootstrap: %d %s", response.Code, response.Body.String())
	}
	cookie := bootstrapAdmin.cookie
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.MaxAge < 14000 || cookie.MaxAge > 14400 {
		t.Fatalf("editorial cookie attributes: %+v", cookie)
	}
	if response := mlAdmin.do(http.MethodPost, "/api/v1/editorial/bootstrap", nil); response.Code != http.StatusConflict {
		t.Fatalf("second bootstrap: %d", response.Code)
	}
	// A stolen/leftover cookie does not work for another signed-in identity.
	mlAdmin.cookie = cookie
	if response := mlAdmin.do(http.MethodGet, "/api/v1/editorial/posts", nil); response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "editorial_session_mismatch") {
		t.Fatalf("cookie reuse across identities: %d %s", response.Code, response.Body.String())
	}
	for subject, role := range map[string]string{"ana": api.EditorialAuthor, "eve": api.EditorialEditor} {
		if response := bootstrapAdmin.do(http.MethodPut, "/api/v1/editorial/members/"+subject, map[string]string{"role": role, "display_name": strings.ToUpper(subject)}); response.Code != http.StatusOK {
			t.Fatalf("grant %s: %d %s", subject, response.Code, response.Body.String())
		}
	}
	for _, client := range []*editorialClient{ana, eve} {
		if response := client.do(http.MethodPost, "/api/v1/editorial/session", nil); response.Code != http.StatusCreated {
			t.Fatalf("%s session: %d %s", client.subject, response.Code, response.Body.String())
		}
	}
	if response := ana.do(http.MethodPut, "/api/v1/editorial/members/zed", map[string]string{"role": "editor"}); response.Code != http.StatusForbidden {
		t.Fatalf("author managed members: %d", response.Code)
	}

	// Author uploads an image (sniffed, not trusted by filename).
	upload, contentType := multipartImage(t, testJPEG(t, 1000, 500), "Gateway request flow")
	response = ana.do(http.MethodPost, "/api/v1/editorial/media", upload, contentType)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", response.Code, response.Body.String())
	}
	uploaded := decodeInto[struct {
		Success int            `json:"success"`
		File    map[string]any `json:"file"`
		Media   api.BlogMedia  `json:"media"`
	}](t, response)
	mediaURL := uploaded.File["url"].(string)
	if uploaded.Success != 1 || uploaded.Media.Status != "pending" || !strings.HasPrefix(mediaURL, "/media/blog/med-") {
		t.Fatalf("upload body %+v", uploaded)
	}
	if response := anonymous.do(http.MethodGet, mediaURL, nil); response.Code != http.StatusNotFound {
		t.Fatalf("pending media public: %d", response.Code)
	}
	if response := ana.do(http.MethodGet, mediaURL, nil); response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/jpeg" || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("member media view: %d %v", response.Code, response.Header())
	}
	svg, svgType := multipartImage(t, []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`), "x")
	if response := ana.do(http.MethodPost, "/api/v1/editorial/media", svg, svgType); response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("svg upload: %d", response.Code)
	}
	big, bigType := multipartImage(t, make([]byte, 10<<20+10), "x")
	if response := ana.do(http.MethodPost, "/api/v1/editorial/media", big, bigType); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload: %d", response.Code)
	}

	// Author writes, autosaves, conflicts, submits.
	imageData, _ := json.Marshal(map[string]any{"media_id": uploaded.Media.ID, "alt": "Gateway request flow", "caption": "How a request moves"})
	blocks := append(paragraphs(60), api.Block{ID: "img1", Type: "image", Data: imageData})
	response = ana.do(http.MethodPost, "/api/v1/editorial/posts", api.SavePostRequest{Title: "Designing the gateway", Summary: "How the Kionga gateway routes and authorizes requests.", Tags: []string{"Go", "Security"}, Blocks: blocks})
	if response.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	post := decodeInto[postView](t, response)
	if post.Revision != 1 || post.Author != "ANA" || !post.CanEdit || len(post.Actions) != 1 || post.Actions[0] != "submit" {
		t.Fatalf("created %+v", post)
	}
	save := api.SavePostRequest{BaseRevision: 1, Title: "Designing the Kionga gateway", Summary: post.Summary, Tags: post.Tags, Blocks: blocks}
	if response := ana.do(http.MethodPut, "/api/v1/editorial/posts/"+post.ID, save); response.Code != http.StatusOK {
		t.Fatalf("autosave: %d %s", response.Code, response.Body.String())
	}
	save.Title = "Stale tab edit"
	response = ana.do(http.MethodPut, "/api/v1/editorial/posts/"+post.ID, save)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"revision":2`) || !strings.Contains(response.Body.String(), "Designing the Kionga gateway") {
		t.Fatalf("conflict: %d %s", response.Code, response.Body.String())
	}
	if response := ana.do(http.MethodGet, "/api/v1/editorial/posts/"+post.ID+"/compare?from=1&to=2", nil); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"field":"title"`) {
		t.Fatalf("compare: %d %s", response.Code, response.Body.String())
	}
	if response := ana.do(http.MethodPost, "/api/v1/editorial/posts/"+post.ID+"/transition", api.TransitionRequest{Action: "publish"}); response.Code != http.StatusForbidden {
		t.Fatalf("author published: %d", response.Code)
	}
	if response := ana.do(http.MethodPost, "/api/v1/editorial/posts/"+post.ID+"/transition", api.TransitionRequest{Action: "submit"}); response.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", response.Code, response.Body.String())
	}
	if response := anonymous.do(http.MethodGet, "/api/v1/blogs/designing-the-kionga-gateway", nil); response.Code != http.StatusNotFound {
		t.Fatalf("unpublished post public: %d", response.Code)
	}

	// Editor publishes; the public page renders the image with alt.
	if response := eve.do(http.MethodPost, "/api/v1/editorial/posts/"+post.ID+"/transition", api.TransitionRequest{Action: "publish"}); response.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", response.Code, response.Body.String())
	}
	response = anonymous.do(http.MethodGet, "/api/v1/blogs/designing-the-kionga-gateway", nil)
	public := decodeInto[api.PublicBlogPost](t, response)
	if !strings.Contains(public.RenderedHTML, `src="`+mediaURL+`"`) || !strings.Contains(public.RenderedHTML, `alt="Gateway request flow"`) || !strings.Contains(public.RenderedHTML, "480w") {
		t.Fatalf("public render: %s", public.RenderedHTML)
	}
	if response := anonymous.do(http.MethodGet, mediaURL, nil); response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "public, max-age=300" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("published media: %d %v", response.Code, response.Header())
	}
	if response := anonymous.do(http.MethodGet, "/api/v1/blogs?author=ANA", nil); !strings.Contains(response.Body.String(), post.ID) {
		t.Fatalf("author filter: %s", response.Body.String())
	}

	// Removing a member revokes their live session immediately.
	if response := bootstrapAdmin.do(http.MethodDelete, "/api/v1/editorial/members/eve", nil); response.Code != http.StatusNoContent {
		t.Fatalf("remove member: %d", response.Code)
	}
	if response := eve.do(http.MethodGet, "/api/v1/editorial/posts", nil); response.Code != http.StatusForbidden {
		t.Fatalf("removed member kept access: %d", response.Code)
	}
	// Cross-site mutation attempts are refused even with a cookie.
	request := newTestRequest(http.MethodPost, "/api/v1/editorial/posts", strings.NewReader(`{}`))
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	request.AddCookie(ana.cookie)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "ana", Roles: []string{auth.RoleUser}}))
	cross := httptest.NewRecorder()
	server.ServeHTTP(cross, request)
	if cross.Code != http.StatusForbidden {
		t.Fatalf("cross-site mutation: %d", cross.Code)
	}
	// Logging out clears the cookie and the server-side session.
	if response := ana.do(http.MethodDelete, "/api/v1/editorial/session", nil); response.Code != http.StatusNoContent || ana.cookie.MaxAge >= 0 {
		t.Fatalf("logout: %d %+v", response.Code, ana.cookie)
	}
}

func TestEditorialPathsRejectMachineIdentities(t *testing.T) {
	t.Setenv("KIONGA_BLOG_AUTHORING_ENABLED", "true")
	if auth.Allowed(auth.Principal{Subject: "svc", Roles: []string{auth.RoleService}}, http.MethodGet, "/api/v1/editorial/posts") {
		t.Fatal("service identity reaches editorial API")
	}
	if auth.Allowed(auth.Principal{Subject: "u", Roles: []string{auth.RoleAdmin}, Credential: "api_token"}, http.MethodGet, "/api/v1/editorial/posts") {
		t.Fatal("API token reaches editorial API")
	}
	if !auth.Allowed(auth.Principal{Subject: "u", Roles: []string{auth.RoleUser}}, http.MethodGet, "/api/v1/editorial/posts") {
		t.Fatal("people must reach the editorial API so membership decides")
	}
}

func TestBlogAuthoringIsOffByDefault(t *testing.T) {
	t.Setenv("KIONGA_BLOG_AUTHORING_ENABLED", "")
	static, _ := fs.Sub(fstest.MapFS{"studio.html": &fstest.MapFile{Data: []byte("studio")}, "console/blogs.html": &fstest.MapFile{Data: []byte("publishing")}, "console.html": &fstest.MapFile{Data: []byte("console")}}, ".")
	repository := store.New()
	server := New(repository, static)
	if posts := repository.BlogPosts(); len(posts) != 0 {
		t.Fatalf("a disabled install must not seed posts: %d", len(posts))
	}
	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/blogs"}, {http.MethodGet, "/api/v1/blogs/hello"}, {http.MethodGet, "/api/v1/editorial/posts"},
		{http.MethodPost, "/api/v1/editorial/session"}, {http.MethodGet, "/media/blog/med-1/960"}, {http.MethodGet, "/studio"},
		{http.MethodGet, "/studio/posts"}, {http.MethodGet, "/studio.txt"}, {http.MethodGet, "/console/blogs"},
	} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, newTestRequest(request.method, request.path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s %s with authoring off: %d", request.method, request.path, response.Code)
		}
	}
	for _, path := range []string{"/console", "/api/v1/health"} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, newTestRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s must stay available: %d", path, response.Code)
		}
	}
	site := httptest.NewRecorder()
	server.ServeHTTP(site, newTestRequest(http.MethodGet, "/api/v1/site", nil))
	if !strings.Contains(site.Body.String(), `"blog_authoring":false`) {
		t.Fatalf("site: %s", site.Body.String())
	}
}

func TestBlogAuthoringCanBeEnabled(t *testing.T) {
	t.Setenv("KIONGA_BLOG_AUTHORING_ENABLED", "true")
	static, _ := fs.Sub(fstest.MapFS{"studio.html": &fstest.MapFile{Data: []byte("studio")}, "console/blogs.html": &fstest.MapFile{Data: []byte("publishing")}}, ".")
	server := New(store.New(), static)
	for _, path := range []string{"/api/v1/blogs", "/studio", "/console/blogs", "/api/v1/site"} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, newTestRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s with authoring on: %d %s", path, response.Code, response.Body.String())
		}
		if path == "/api/v1/site" && !strings.Contains(response.Body.String(), `"blog_authoring":true`) {
			t.Fatalf("site: %s", response.Body.String())
		}
	}
	if !blogPath("/studio/edit") || blogPath("/studios") || blogPath("/api/v1/blogsx") || !blogPath("/media/blog/x") {
		t.Fatal("blogPath must match the feature's routes exactly")
	}
}
