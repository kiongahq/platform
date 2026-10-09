package editorial

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newService(t *testing.T) (*Service, *clock) {
	t.Helper()
	c := &clock{now: time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)}
	return &Service{Docs: store.New(), Media: &FSStore{Dir: t.TempDir()}, Now: c.Now}, c
}

func seedMembers(t *testing.T, s *Service) {
	t.Helper()
	if _, err := s.Bootstrap(api.EditorialMember{Subject: "ada", DisplayName: "Ada"}, "admin"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []api.EditorialMember{author, otherAuthor, editor} {
		m.DisplayName = strings.ToUpper(m.Subject)
		if _, err := s.PutMember(admin, m); err != nil {
			t.Fatal(err)
		}
	}
}

func draftRequest(title string) api.SavePostRequest {
	return api.SavePostRequest{Title: title, Summary: "A summary long enough to publish later.", Tags: []string{"Go"},
		Blocks: []api.Block{block("paragraph", map[string]any{"text": strings.Repeat("content ", 30)})}}
}

func TestBootstrapOnlyOnceAndLastAdminGuard(t *testing.T) {
	s, _ := newService(t)
	if !s.BootstrapAvailable() {
		t.Fatal("bootstrap unavailable on empty workspace")
	}
	seedMembers(t, s)
	if _, err := s.Bootstrap(api.EditorialMember{Subject: "mallory"}, "admin"); !errors.Is(err, ErrBootstrapClosed) {
		t.Fatalf("second bootstrap: %v", err)
	}
	if _, err := s.PutMember(admin, api.EditorialMember{Subject: "ada", Role: api.EditorialEditor}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demoted last admin: %v", err)
	}
	if err := s.DeleteMember(admin, "ada"); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("removed last admin: %v", err)
	}
	if _, err := s.PutMember(editor, api.EditorialMember{Subject: "x", Role: api.EditorialAuthor}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("editor managed members: %v", err)
	}
}

func TestSessionsRecheckMembership(t *testing.T) {
	s, c := newService(t)
	seedMembers(t, s)
	if _, _, _, err := s.CreateSession("stranger"); !errors.Is(err, ErrNotMember) {
		t.Fatalf("non-member session: %v", err)
	}
	token, _, _, err := s.CreateSession("eve")
	if err != nil {
		t.Fatal(err)
	}
	if member, err := s.ResolveSession(token); err != nil || member.Role != api.EditorialEditor {
		t.Fatalf("resolve: %+v %v", member, err)
	}
	if _, err := s.PutMember(admin, api.EditorialMember{Subject: "eve", Role: api.EditorialEditor, Disabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveSession(token); err == nil {
		t.Fatal("disabled member kept access")
	}
	token, _, _, _ = s.CreateSession("ana")
	c.now = c.now.Add(SessionTTL + time.Second)
	if _, err := s.ResolveSession(token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("expired session accepted: %v", err)
	}
	s.Sweep(context.Background())
	if left := store.ListDocs[session](s.Docs, SessionKind); len(left) != 0 {
		t.Fatalf("expired sessions not swept: %+v", left)
	}
}

func TestAutosaveConflictRevisionsAndRestore(t *testing.T) {
	s, c := newService(t)
	seedMembers(t, s)
	post, err := s.CreatePost(author, draftRequest("First title"))
	if err != nil {
		t.Fatal(err)
	}
	if post.Revision != 1 || post.Slug != "first-title" || post.Author != "ana" {
		t.Fatalf("created %+v", post)
	}
	c.now = c.now.Add(time.Minute)
	request := draftRequest("Second title")
	request.BaseRevision = 1
	saved, wrote, err := s.SavePost(author, post.ID, request)
	if err != nil || !wrote || saved.Revision != 2 || saved.Slug != "second-title" {
		t.Fatalf("save: %+v %v %v", saved, wrote, err)
	}
	// Unchanged autosave is a no-op.
	request.BaseRevision = 2
	same, wrote, err := s.SavePost(author, post.ID, request)
	if err != nil || wrote || same.Revision != 2 {
		t.Fatalf("no-op save wrote: %v %v", wrote, err)
	}
	// A stale base revision is a conflict carrying the latest post.
	stale := draftRequest("Stale tab")
	stale.BaseRevision = 1
	_, _, err = s.SavePost(author, post.ID, stale)
	var conflict ConflictError
	if !errors.As(err, &conflict) || conflict.Latest.Revision != 2 || conflict.Latest.Title != "Second title" {
		t.Fatalf("conflict: %v", err)
	}
	// Old slug is released; the new one is taken.
	if _, err := s.PostBySlug("second-title"); err != nil {
		t.Fatalf("slug lookup: %v", err)
	}
	if _, err := s.CreatePost(otherAuthor, draftRequest("Second title")); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("duplicate slug: %v", err)
	}
	if _, err := s.CreatePost(otherAuthor, draftRequest("First title")); err != nil {
		t.Fatalf("released slug not reusable: %v", err)
	}
	revisions := s.Revisions(post.ID)
	if len(revisions) != 2 || revisions[0].Number != 2 || revisions[1].Reason != "create" {
		t.Fatalf("revisions %+v", revisions)
	}
	comparison := Compare(revisions[1], revisions[0])
	if len(comparison.Fields) == 0 || comparison.Fields[0].Field != "title" {
		t.Fatalf("compare %+v", comparison)
	}
	// Restore writes a new revision with the old content.
	restored, err := s.Restore(author, post.ID, 1, 2)
	if err != nil || restored.Revision != 3 || restored.Title != "First title" {
		t.Fatalf("restore: %+v %v", restored, err)
	}
	if rev, _ := s.Revision(post.ID, 3); rev.Reason != "restore" || rev.RestoredFrom != 1 {
		t.Fatalf("restore revision %+v", rev)
	}
	if rev, _ := s.Revision(post.ID, 1); rev.Title != "First title" || rev.Reason != "create" {
		t.Fatalf("revision 1 mutated: %+v", rev)
	}
	// Other authors cannot edit; submitted posts are locked for the author.
	if _, _, err := s.SavePost(otherAuthor, post.ID, request); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other author saved: %v", err)
	}
	if _, err := s.Transition(author, post.ID, api.TransitionRequest{Action: ActionSubmit}); err != nil {
		t.Fatal(err)
	}
	request.BaseRevision = 3
	if _, _, err := s.SavePost(author, post.ID, request); !errors.Is(err, ErrTransition) {
		t.Fatalf("author edited in-review post: %v", err)
	}
	if _, err := s.Transition(author, post.ID, api.TransitionRequest{Action: ActionPublish}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("author published: %v", err)
	}
	published, err := s.Transition(editor, post.ID, api.TransitionRequest{Action: ActionPublish})
	if err != nil || published.Status != api.PostPublished || published.PublishedAt == nil {
		t.Fatalf("publish: %+v %v", published, err)
	}
}

func TestScheduledPublishWithFakeClockAndLease(t *testing.T) {
	s, c := newService(t)
	seedMembers(t, s)
	post, _ := s.CreatePost(author, draftRequest("Scheduled post"))
	at := c.now.Add(2 * time.Hour)
	if _, err := s.Transition(editor, post.ID, api.TransitionRequest{Action: ActionSchedule, PublishAt: &at}); err != nil {
		t.Fatal(err)
	}
	a := &Publisher{Service: s, Holder: "replica-a"}
	b := &Publisher{Service: s, Holder: "replica-b"}
	if got, err := a.Tick(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("published early: %v %v", got, err)
	}
	if _, err := b.Tick(context.Background()); !errors.Is(err, errNotLeader) {
		t.Fatalf("second replica not fenced: %v", err)
	}
	c.now = at.Add(time.Second)
	got, err := a.Tick(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("due post not published: %v %v", got, err)
	}
	again, _ := a.Tick(context.Background())
	if len(again) != 0 {
		t.Fatal("published twice")
	}
	saved, _ := s.Post(post.ID)
	if saved.Status != api.PostPublished || !saved.PublishedAt.Equal(at) || saved.PublishAt != nil {
		t.Fatalf("scheduled publish state %+v", saved)
	}
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, img, nil); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestMediaAttachVisibilityAndSweeper(t *testing.T) {
	s, c := newService(t)
	seedMembers(t, s)
	ctx := context.Background()
	kept, err := s.Upload(ctx, author, jpegBytes(t, 1200, 600), "A diagram", "", "Photo: Kionga")
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := s.Upload(ctx, author, jpegBytes(t, 300, 200), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if kept.Status != "pending" || len(kept.Variants) != 3 || kept.Variants[1].Width != 960 || kept.Variants[2].Width != 1200 {
		t.Fatalf("variants %+v", kept.Variants)
	}
	if len(orphan.Variants) != 1 || orphan.Variants[0].Width != 300 {
		t.Fatalf("small image variants %+v", orphan.Variants)
	}
	request := draftRequest("Post with image")
	request.Blocks = append(request.Blocks, block("image", map[string]any{"media_id": kept.ID, "alt": "A diagram"}))
	post, err := s.CreatePost(author, request)
	if err != nil {
		t.Fatal(err)
	}
	attached, _ := s.MediaItem(kept.ID)
	if attached.Status != "attached" || attached.PostIDs[0] != post.ID {
		t.Fatalf("attach %+v", attached)
	}
	if s.MediaPublic(attached) {
		t.Fatal("media public before the post is published")
	}
	if _, err := s.Transition(editor, post.ID, api.TransitionRequest{Action: ActionPublish}); err != nil {
		t.Fatal(err)
	}
	if !s.MediaPublic(attached) {
		t.Fatal("media not public after publish")
	}
	unknown := draftRequest("Missing media")
	unknown.Blocks = append(unknown.Blocks, block("image", map[string]any{"media_id": "med-ffffffff", "alt": "x"}))
	if _, err := s.CreatePost(author, unknown); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatalf("unknown media accepted: %v", err)
	}
	c.now = c.now.Add(PendingMediaTTL + time.Minute)
	if removed := s.Sweep(ctx); removed != 1 {
		t.Fatalf("swept %d", removed)
	}
	if _, err := s.MediaItem(orphan.ID); err == nil {
		t.Fatal("orphan survived")
	}
	if _, err := s.Media.Get(ctx, orphan.ID+"/480"); !errors.Is(err, ErrMediaMissing) {
		t.Fatalf("orphan bytes survived: %v", err)
	}
	if _, err := s.Media.Get(ctx, kept.ID+"/960"); err != nil {
		t.Fatalf("attached media swept: %v", err)
	}
}

func TestMigrationPreservesSlugsAndIsIdempotent(t *testing.T) {
	s, _ := newService(t)
	published := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	legacy := []api.BlogPost{
		{ID: "blog-1", Slug: "mounting-s3", Title: "Mounting S3", Summary: "Summary text that is long", Content: "# Mounting\n\nBody with **bold**.", Author: "Kionga Engineering", Tags: []string{"S3"}, Status: "published", CreatedAt: published, UpdatedAt: published, PublishedAt: &published},
		{ID: "blog-2", Slug: "draft-notes", Title: "Draft notes", Summary: "Another summary text", Content: "Plain", Author: "Someone", Status: "draft", CreatedAt: published, UpdatedAt: published},
	}
	count, err := s.MigrateLegacy(legacy)
	if err != nil || count != 2 {
		t.Fatalf("migrate: %d %v", count, err)
	}
	post, err := s.PostBySlug("mounting-s3")
	if err != nil || post.ID != "blog-1" || post.Status != api.PostPublished || !post.PublishedAt.Equal(published) || post.Author != "Kionga Engineering" || post.LegacyMarkdown == "" || len(post.Blocks) != 2 {
		t.Fatalf("migrated %+v %v", post, err)
	}
	if draft, _ := s.PostBySlug("draft-notes"); draft.Status != api.PostDraft {
		t.Fatalf("draft status %+v", draft)
	}
	// Archive then migrate again: nothing is recreated or reverted.
	if _, err := s.Transition(editor, "blog-1", api.TransitionRequest{Action: ActionArchive}); err != nil {
		t.Fatal(err)
	}
	if count, err := s.MigrateLegacy(legacy); err != nil || count != 0 {
		t.Fatalf("second migration: %d %v", count, err)
	}
	if post, _ := s.Post("blog-1"); post.Status != api.PostArchived {
		t.Fatalf("migration reverted archive: %s", post.Status)
	}
	if len(s.Revisions("blog-1")) != 1 {
		t.Fatal("migration revision missing")
	}
}
