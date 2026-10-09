package store

import (
	"testing"

	"github.com/kiongahq/platform/pkg/api"
)

func TestBlogPostLifecycle(t *testing.T) {
	repository := New()
	request := api.UpsertBlogPostRequest{
		Title: "Operating a reliable feature store", Summary: "A sufficiently detailed summary for an engineering article.",
		Content: "# Feature stores\n\n" + string(make([]byte, 120)), Author: "Engineering", Tags: []string{"MLOps"}, Status: "draft",
	}
	post, err := repository.UpsertBlogPost("", request, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if post.Slug != "operating-a-reliable-feature-store" || post.PublishedAt != nil {
		t.Fatalf("unexpected draft: %+v", post)
	}
	request.Status = "published"
	published, err := repository.UpsertBlogPost(post.ID, request, "admin")
	if err != nil || published.PublishedAt == nil {
		t.Fatalf("publish: %+v %v", published, err)
	}
	if err := repository.DeleteBlogPost(post.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BlogPost(post.ID); err == nil {
		t.Fatal("deleted post still exists")
	}
}
