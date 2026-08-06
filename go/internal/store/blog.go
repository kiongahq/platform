package store

import (
	"errors"
	"strings"
	"time"

	"github.com/ml-ai-ops/platform/pkg/api"
)

func validateBlog(req api.UpsertBlogPostRequest) (api.UpsertBlogPostRequest, error) {
	req.Slug = slug(req.Slug)
	req.Title, req.Summary, req.Content = strings.TrimSpace(req.Title), strings.TrimSpace(req.Summary), strings.TrimSpace(req.Content)
	req.Author, req.Tags = strings.TrimSpace(req.Author), unique(req.Tags)
	if len(req.Title) < 5 || len(req.Summary) < 20 || len(req.Content) < 100 {
		return req, errors.New("title, summary, and substantial content are required")
	}
	if req.Slug == "" {
		req.Slug = slug(req.Title)
	}
	if req.Author == "" {
		req.Author = "Kionga Engineering"
	}
	if req.Status == "" {
		req.Status = "draft"
	}
	if req.Status != "draft" && req.Status != "published" {
		return req, errors.New("status must be draft or published")
	}
	return req, nil
}

func (s *Store) BlogPosts() []api.BlogPost {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.data.BlogPosts)
}

func (s *Store) BlogPost(identifier string) (api.BlogPost, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, post := range s.data.BlogPosts {
		if post.ID == identifier || post.Slug == identifier {
			return post, nil
		}
	}
	return api.BlogPost{}, ErrNotFound
}

func (s *Store) UpsertBlogPost(postID string, req api.UpsertBlogPostRequest, actor string) (api.BlogPost, error) {
	req, err := validateBlog(req)
	if err != nil {
		return api.BlogPost{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for _, post := range s.data.BlogPosts {
		if post.Slug == req.Slug && post.ID != postID {
			return api.BlogPost{}, ErrConflict
		}
	}
	for i := range s.data.BlogPosts {
		if s.data.BlogPosts[i].ID == postID {
			post := &s.data.BlogPosts[i]
			post.Slug, post.Title, post.Summary, post.Content = req.Slug, req.Title, req.Summary, req.Content
			post.Author, post.Tags, post.Status, post.UpdatedAt = req.Author, req.Tags, req.Status, now
			if req.Status == "published" && post.PublishedAt == nil {
				post.PublishedAt = &now
			}
			s.record("blog.updated", "blog_post", post.ID, actor, map[string]any{"status": post.Status})
			return *post, s.persist()
		}
	}
	post := api.BlogPost{ID: id("blog"), Slug: req.Slug, Title: req.Title, Summary: req.Summary, Content: req.Content, Author: req.Author, Tags: req.Tags, Status: req.Status, CreatedAt: now, UpdatedAt: now}
	if post.Status == "published" {
		post.PublishedAt = &now
	}
	s.data.BlogPosts = append([]api.BlogPost{post}, s.data.BlogPosts...)
	s.record("blog.created", "blog_post", post.ID, actor, map[string]any{"status": post.Status})
	return post, s.persist()
}

func (s *Store) DeleteBlogPost(postID, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.BlogPosts {
		if s.data.BlogPosts[i].ID == postID {
			s.data.BlogPosts = append(s.data.BlogPosts[:i], s.data.BlogPosts[i+1:]...)
			s.record("blog.deleted", "blog_post", postID, actor, nil)
			return s.persist()
		}
	}
	return ErrNotFound
}
