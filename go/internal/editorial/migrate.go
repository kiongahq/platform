package editorial

import (
	"errors"

	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

// MigrateLegacy converts Markdown blog posts (the pre-editorial blog_post
// records, seeds included) into block posts exactly once. Each legacy post
// keeps its id, slug, author, status and dates; its Markdown is preserved in
// legacy_markdown. A marker document per legacy id makes the migration
// idempotent across restarts and replicas, and keeps an archived or edited
// migrated post from being recreated.
func (s *Service) MigrateLegacy(legacy []api.BlogPost) (int, error) {
	migrated := 0
	var errs []error
	for _, old := range legacy {
		if _, err := s.Docs.GetDocument(MigrationKind, old.ID); err == nil {
			continue
		}
		if _, err := s.Post(old.ID); errors.Is(err, store.ErrNotFound) {
			if err := s.migrateOne(old); err != nil {
				errs = append(errs, err)
				continue
			}
			migrated++
		}
		_, err := store.UpdateDoc(s.Docs, MigrationKind, old.ID, func(current map[string]string, exists bool) (map[string]string, error) {
			if exists {
				return current, store.ErrSkipWrite
			}
			return map[string]string{"legacy_id": old.ID, "slug": old.Slug}, nil
		}, "editorial.migration.recorded", "editorial-migration")
		if err != nil {
			errs = append(errs, err)
		}
	}
	return migrated, errors.Join(errs...)
}

func (s *Service) migrateOne(old api.BlogPost) error {
	blocks, err := NormalizeBlocks(FromMarkdown(old.Content))
	if err != nil {
		return err
	}
	status := api.PostDraft
	if old.Status == api.PostPublished {
		status = api.PostPublished
	}
	slug := old.Slug
	if slug == "" {
		slug = Slugify(old.Title)
	}
	if _, err := s.claimSlug(slug, old.ID, "editorial-migration"); err != nil {
		return err
	}
	tags := NormalizeTags(old.Tags)
	post := api.EditorialPost{
		ID: old.ID, Slug: slug, Title: old.Title, Summary: old.Summary, Blocks: blocks, Tags: tags,
		Author: old.Author, Status: status, Revision: 1, PublishedAt: old.PublishedAt,
		CreatedAt: old.CreatedAt, UpdatedAt: old.UpdatedAt, UpdatedBy: "editorial-migration",
		LegacyMarkdown: old.Content, MigratedFrom: "blog_post",
	}
	post.ContentHash = hashOf(cleanedPost{title: post.Title, summary: post.Summary, slug: post.Slug, tags: tags, blocks: blocks})
	created := false
	saved, err := store.UpdateDoc(s.Docs, PostKind, old.ID, func(current api.EditorialPost, exists bool) (api.EditorialPost, error) {
		if exists {
			return current, store.ErrSkipWrite
		}
		created = true
		return post, nil
	}, "editorial.post.migrated", "editorial-migration")
	if err != nil {
		return err
	}
	if created {
		s.writeRevision(saved, "migration", 0, "editorial-migration")
	}
	return nil
}
