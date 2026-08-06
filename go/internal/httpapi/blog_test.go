package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ml-ai-ops/platform/pkg/api"
)

func TestSeedEngineeringBlogIsPublic(t *testing.T) {
	server := testServer()
	list := httptest.NewRecorder()
	server.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/blogs", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "mounting-s3-as-a-filesystem-in-jupyter") {
		t.Fatalf("blog list: %d %s", list.Code, list.Body.String())
	}
	post := httptest.NewRecorder()
	server.ServeHTTP(post, httptest.NewRequest(http.MethodGet, "/api/v1/blogs/mounting-s3-as-a-filesystem-in-jupyter", nil))
	if post.Code != http.StatusOK || !strings.Contains(post.Body.String(), "Production checklist") {
		t.Fatalf("blog post: %d %s", post.Code, post.Body.String())
	}
}

func TestDraftIsAdminOnlyUntilPublished(t *testing.T) {
	server := testServer()
	body := `{"title":"Testing platform upgrades safely","summary":"A detailed summary of safe platform upgrade practices.","content":"# Safe upgrades\n\nThis article has enough substantial content to pass validation and remain a useful draft for later publication. It includes rollout and rollback concerns.","author":"Kionga Engineering","tags":["Operations"],"status":"draft"}`
	create := httptest.NewRecorder()
	server.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/v1/admin/blogs", strings.NewReader(body)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	var post api.BlogPost
	if err := json.Unmarshal(create.Body.Bytes(), &post); err != nil {
		t.Fatal(err)
	}
	public := httptest.NewRecorder()
	server.ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/api/v1/blogs/"+post.Slug, nil))
	if public.Code != http.StatusNotFound {
		t.Fatalf("draft leaked publicly: %d", public.Code)
	}
	admin := httptest.NewRecorder()
	server.ServeHTTP(admin, httptest.NewRequest(http.MethodGet, "/api/v1/admin/blogs", nil))
	if admin.Code != http.StatusOK || !strings.Contains(admin.Body.String(), post.ID) {
		t.Fatalf("admin list: %d %s", admin.Code, admin.Body.String())
	}
}
