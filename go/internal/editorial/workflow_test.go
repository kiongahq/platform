package editorial

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ml-ai-ops/platform/pkg/api"
)

var (
	author      = api.EditorialMember{Subject: "ana", Role: api.EditorialAuthor}
	otherAuthor = api.EditorialMember{Subject: "ben", Role: api.EditorialAuthor}
	editor      = api.EditorialMember{Subject: "eve", Role: api.EditorialEditor}
	admin       = api.EditorialMember{Subject: "ada", Role: api.EditorialAdmin}
)

func readyPost(status string) api.EditorialPost {
	return api.EditorialPost{ID: "post-1", AuthorSubject: "ana", Status: status, Title: "A real title", Summary: "A summary that is long enough to publish.",
		Blocks: []api.Block{block("paragraph", map[string]any{"text": strings.Repeat("words ", 30)})}}
}

func TestTransitionMatrixPerRole(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	cases := []struct {
		member api.EditorialMember
		from   string
		action string
		want   string
		err    error
	}{
		{author, api.PostDraft, ActionSubmit, api.PostInReview, nil},
		{otherAuthor, api.PostDraft, ActionSubmit, "", ErrForbidden},
		{author, api.PostInReview, ActionWithdraw, api.PostDraft, nil},
		{author, api.PostDraft, ActionPublish, "", ErrForbidden},
		{author, api.PostInReview, ActionPublish, "", ErrForbidden},
		{author, api.PostPublished, ActionUnpublish, "", ErrForbidden},
		{author, api.PostDraft, ActionArchive, "", ErrForbidden},
		{author, api.PostDraft, ActionSchedule, "", ErrForbidden},
		{editor, api.PostInReview, ActionPublish, api.PostPublished, nil},
		{editor, api.PostInReview, ActionRequestChanges, api.PostDraft, nil},
		{editor, api.PostDraft, ActionPublish, api.PostPublished, nil},
		{editor, api.PostInReview, ActionSchedule, api.PostScheduled, nil},
		{editor, api.PostPublished, ActionUnpublish, api.PostDraft, nil},
		{editor, api.PostScheduled, ActionUnpublish, api.PostDraft, nil},
		{editor, api.PostPublished, ActionArchive, api.PostArchived, nil},
		{editor, api.PostArchived, ActionRestore, api.PostDraft, nil},
		{editor, api.PostPublished, ActionPublish, "", ErrTransition},
		{editor, api.PostArchived, ActionPublish, "", ErrTransition},
		{editor, api.PostDraft, ActionUnpublish, "", ErrTransition},
		{admin, api.PostInReview, ActionPublish, api.PostPublished, nil},
		{api.EditorialMember{Subject: "eve", Role: api.EditorialEditor, Disabled: true}, api.PostInReview, ActionPublish, "", ErrForbidden},
		{api.EditorialMember{Subject: "ml-admin", Role: "admin"}, api.PostInReview, ActionPublish, "", ErrForbidden},
	}
	for _, c := range cases {
		post := readyPost(c.from)
		got, err := Apply(c.member, post, api.TransitionRequest{Action: c.action, PublishAt: &future}, "", now)
		if c.err != nil {
			if !errors.Is(err, c.err) {
				t.Errorf("%s %s from %s: err %v, want %v", c.member.Role, c.action, c.from, err, c.err)
			}
			continue
		}
		if err != nil || got.Status != c.want {
			t.Errorf("%s %s from %s: got %s err %v, want %s", c.member.Role, c.action, c.from, got.Status, err, c.want)
		}
	}
}

func TestPublishRequiresAltTextAndContent(t *testing.T) {
	now := time.Now()
	post := readyPost(api.PostInReview)
	post.Blocks = append(post.Blocks, block("image", map[string]any{"media_id": "med-abcdef12", "alt": ""}))
	if _, err := Apply(editor, post, api.TransitionRequest{Action: ActionPublish}, "", now); err == nil || !strings.Contains(err.Error(), "alt text") {
		t.Fatalf("missing alt accepted: %v", err)
	}
	post = readyPost(api.PostInReview)
	post.Cover = &api.PostCover{MediaID: "med-abcdef12"}
	if _, err := Apply(editor, post, api.TransitionRequest{Action: ActionPublish}, "", now); err == nil {
		t.Fatal("cover without alt accepted")
	}
	if _, err := Apply(editor, post, api.TransitionRequest{Action: ActionPublish}, "media library alt", now); err != nil {
		t.Fatalf("cover alt from media library rejected: %v", err)
	}
	empty := readyPost(api.PostInReview)
	empty.Blocks = nil
	if _, err := Apply(editor, empty, api.TransitionRequest{Action: ActionPublish}, "", now); err == nil {
		t.Fatal("empty post published")
	}
}

func TestScheduleNeedsFutureTime(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Minute)
	if _, err := Apply(editor, readyPost(api.PostInReview), api.TransitionRequest{Action: ActionSchedule, PublishAt: &past}, "", now); err == nil {
		t.Fatal("past schedule accepted")
	}
	if _, err := Apply(editor, readyPost(api.PostInReview), api.TransitionRequest{Action: ActionSchedule}, "", now); err == nil {
		t.Fatal("schedule without time accepted")
	}
}

func TestEditRightsPerRole(t *testing.T) {
	if CanEdit(author, readyPost(api.PostDraft)) != nil {
		t.Fatal("author cannot edit own draft")
	}
	if !errors.Is(CanEdit(otherAuthor, readyPost(api.PostDraft)), ErrForbidden) {
		t.Fatal("author edits someone else's draft")
	}
	if !errors.Is(CanEdit(author, readyPost(api.PostInReview)), ErrTransition) {
		t.Fatal("author edits a post under review")
	}
	if CanEdit(editor, readyPost(api.PostPublished)) != nil {
		t.Fatal("editor cannot fix a published post")
	}
	if !errors.Is(CanEdit(editor, readyPost(api.PostArchived)), ErrTransition) {
		t.Fatal("archived post editable")
	}
	if got := Actions(author, readyPost(api.PostDraft)); !slices.Equal(got, []string{ActionSubmit}) {
		t.Fatalf("author actions %v", got)
	}
	if got := Actions(editor, readyPost(api.PostInReview)); !slices.Contains(got, ActionPublish) || !slices.Contains(got, ActionRequestChanges) {
		t.Fatalf("editor actions %v", got)
	}
}

func TestRelatedByTagOverlap(t *testing.T) {
	at := func(d int) *time.Time { v := time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC); return &v }
	post := api.EditorialPost{ID: "p", Tags: []string{"Go", "S3", "Jupyter"}}
	candidates := []api.EditorialPost{
		{ID: "a", Slug: "a", Status: api.PostPublished, Tags: []string{"go"}, PublishedAt: at(5)},
		{ID: "b", Slug: "b", Status: api.PostPublished, Tags: []string{"Go", "S3"}, PublishedAt: at(1)},
		{ID: "c", Slug: "c", Status: api.PostDraft, Tags: []string{"Go", "S3", "Jupyter"}},
		{ID: "d", Slug: "d", Status: api.PostPublished, Tags: []string{"Python"}},
		{ID: "e", Slug: "e", Status: api.PostPublished, Tags: []string{"Jupyter"}, PublishedAt: at(9)},
	}
	if got := RelatedByTags(post, candidates, 3); !slices.Equal(got, []string{"b", "e", "a"}) {
		t.Fatalf("related %v", got)
	}
}

func TestSlugifyAndTags(t *testing.T) {
	if got := Slugify("  Mounting S3 — as a FS!  "); got != "mounting-s3-as-a-fs" {
		t.Fatalf("slug %q", got)
	}
	if got := NormalizeTags([]string{"Go", "go", " ", "<b>S3</b>"}); !slices.Equal(got, []string{"Go", "S3"}) {
		t.Fatalf("tags %v", got)
	}
}
