package editorial

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ml-ai-ops/platform/pkg/api"
)

// Workflow errors. Handlers map ErrForbidden to 403 and ErrTransition to 409.
var (
	ErrForbidden  = errors.New("your editorial role does not allow this")
	ErrTransition = errors.New("this action is not valid for the post's current status")
)

var roleRank = map[string]int{api.EditorialAuthor: 1, api.EditorialEditor: 2, api.EditorialAdmin: 3}

// ValidRole reports whether role is one of the editorial roles.
func ValidRole(role string) bool { return roleRank[role] > 0 }

// AtLeast reports whether role includes the rights of minimum. Roles are
// cumulative: editorial_admin > editor > author.
func AtLeast(role, minimum string) bool {
	return roleRank[role] >= roleRank[minimum] && roleRank[role] > 0
}

// CanEdit decides whether a member may save content on a post. Authors edit
// only their own drafts; editors edit anything that is not archived.
func CanEdit(member api.EditorialMember, post api.EditorialPost) error {
	switch {
	case member.Disabled || !ValidRole(member.Role):
		return ErrForbidden
	case post.Status == api.PostArchived:
		return ErrTransition
	case AtLeast(member.Role, api.EditorialEditor):
		return nil
	case post.AuthorSubject != member.Subject:
		return ErrForbidden
	case post.Status != api.PostDraft:
		return ErrTransition
	}
	return nil
}

// CanView decides read access inside the workspace: authors see their own
// posts plus everything published; editors see all.
func CanView(member api.EditorialMember, post api.EditorialPost) bool {
	if member.Disabled || !ValidRole(member.Role) {
		return false
	}
	return AtLeast(member.Role, api.EditorialEditor) || post.AuthorSubject == member.Subject || post.Status == api.PostPublished
}

// Transition actions.
const (
	ActionSubmit         = "submit"
	ActionWithdraw       = "withdraw"
	ActionRequestChanges = "request_changes"
	ActionPublish        = "publish"
	ActionSchedule       = "schedule"
	ActionUnpublish      = "unpublish"
	ActionArchive        = "archive"
	ActionRestore        = "restore"
)

type rule struct {
	from    []string
	to      string
	minimum string
	owner   bool // authors may act on their own posts only
}

var rules = map[string]rule{
	ActionSubmit:         {from: []string{api.PostDraft}, to: api.PostInReview, minimum: api.EditorialAuthor, owner: true},
	ActionWithdraw:       {from: []string{api.PostInReview}, to: api.PostDraft, minimum: api.EditorialAuthor, owner: true},
	ActionRequestChanges: {from: []string{api.PostInReview}, to: api.PostDraft, minimum: api.EditorialEditor},
	ActionPublish:        {from: []string{api.PostDraft, api.PostInReview, api.PostScheduled}, to: api.PostPublished, minimum: api.EditorialEditor},
	ActionSchedule:       {from: []string{api.PostDraft, api.PostInReview, api.PostScheduled}, to: api.PostScheduled, minimum: api.EditorialEditor},
	ActionUnpublish:      {from: []string{api.PostPublished, api.PostScheduled}, to: api.PostDraft, minimum: api.EditorialEditor},
	ActionArchive:        {from: []string{api.PostDraft, api.PostInReview, api.PostScheduled, api.PostPublished}, to: api.PostArchived, minimum: api.EditorialEditor},
	ActionRestore:        {from: []string{api.PostArchived}, to: api.PostDraft, minimum: api.EditorialEditor},
}

// Actions lists the actions a member may take on a post right now, for the
// UI to render only valid buttons.
func Actions(member api.EditorialMember, post api.EditorialPost) []string {
	var out []string
	for _, name := range []string{ActionSubmit, ActionWithdraw, ActionRequestChanges, ActionPublish, ActionSchedule, ActionUnpublish, ActionArchive, ActionRestore} {
		if allowed(member, post, name) == nil {
			out = append(out, name)
		}
	}
	return out
}

func allowed(member api.EditorialMember, post api.EditorialPost, action string) error {
	r, ok := rules[action]
	if !ok {
		return ValidationError{Message: "unknown action " + action}
	}
	if member.Disabled || !AtLeast(member.Role, r.minimum) {
		return ErrForbidden
	}
	if r.owner && !AtLeast(member.Role, api.EditorialEditor) && post.AuthorSubject != member.Subject {
		return ErrForbidden
	}
	if !slices.Contains(r.from, post.Status) {
		return ErrTransition
	}
	return nil
}

// PublishReady lists what blocks publication: a title, summary, content and
// alt text on every image and the cover.
func PublishReady(post api.EditorialPost, coverAlt string) error {
	var problems []string
	if len(strings.TrimSpace(post.Title)) < 5 {
		problems = append(problems, "a title of at least 5 characters")
	}
	if len(strings.TrimSpace(post.Summary)) < 20 {
		problems = append(problems, "a summary of at least 20 characters")
	}
	if len(strings.Fields(TextOf(post.Blocks))) < 20 {
		problems = append(problems, "at least 20 words of content")
	}
	if missing := MissingAltText(post.Blocks); len(missing) > 0 {
		problems = append(problems, "alt text on every image")
	}
	if post.Cover != nil && strings.TrimSpace(post.Cover.Alt) == "" && strings.TrimSpace(coverAlt) == "" {
		problems = append(problems, "alt text on the cover image")
	}
	if len(problems) > 0 {
		return ValidationError{Message: "Before publishing, add " + strings.Join(problems, ", ") + "."}
	}
	return nil
}

// Apply performs a workflow action on post, returning the updated post. now
// is injected so the scheduled publisher and tests share one clock.
func Apply(member api.EditorialMember, post api.EditorialPost, request api.TransitionRequest, coverAlt string, now time.Time) (api.EditorialPost, error) {
	if err := allowed(member, post, request.Action); err != nil {
		return post, err
	}
	r := rules[request.Action]
	now = now.UTC()
	switch request.Action {
	case ActionSubmit:
		post.SubmittedAt = &now
	case ActionPublish:
		if err := PublishReady(post, coverAlt); err != nil {
			return post, err
		}
		post.PublishAt = nil
		if post.PublishedAt == nil {
			post.PublishedAt = &now
		}
	case ActionSchedule:
		if request.PublishAt == nil || !request.PublishAt.After(now) {
			return post, ValidationError{Message: "Choose a publish time in the future."}
		}
		if err := PublishReady(post, coverAlt); err != nil {
			return post, err
		}
		at := request.PublishAt.UTC()
		post.PublishAt = &at
	case ActionUnpublish:
		post.PublishAt = nil
	case ActionArchive:
		post.ArchivedAt, post.PublishAt = &now, nil
	case ActionRestore:
		post.ArchivedAt = nil
	}
	post.Status, post.UpdatedAt, post.UpdatedBy = r.to, now, member.Subject
	return post, nil
}

// PublishDue moves a scheduled post whose time has come to published. It is
// idempotent: a post that is no longer scheduled or not yet due is returned
// unchanged with ok=false.
func PublishDue(post api.EditorialPost, now time.Time) (api.EditorialPost, bool) {
	if post.Status != api.PostScheduled || post.PublishAt == nil || post.PublishAt.After(now) {
		return post, false
	}
	at := post.PublishAt.UTC()
	post.Status, post.PublishedAt, post.PublishAt = api.PostPublished, &at, nil
	post.UpdatedAt, post.UpdatedBy = now.UTC(), "editorial-scheduler"
	return post, true
}

// Slugify lowers, strips and dashes a title into a URL slug.
func Slugify(value string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 80 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// NormalizeTags trims, dedupes (case-insensitively) and bounds tags.
func NormalizeTags(tags []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.TrimSpace(PlainText(tag))
		if len(tag) > 32 {
			tag = tag[:32]
		}
		key := strings.ToLower(tag)
		if tag == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, tag)
		if len(out) == MaxTags {
			break
		}
	}
	return out
}

// RelatedByTags returns up to limit slugs of posts sharing the most tags with
// post, newest first on ties.
func RelatedByTags(post api.EditorialPost, candidates []api.EditorialPost, limit int) []string {
	type scored struct {
		slug  string
		score int
		at    time.Time
	}
	tags := map[string]bool{}
	for _, tag := range post.Tags {
		tags[strings.ToLower(tag)] = true
	}
	var list []scored
	for _, candidate := range candidates {
		if candidate.ID == post.ID || candidate.Status != api.PostPublished {
			continue
		}
		score := 0
		for _, tag := range candidate.Tags {
			if tags[strings.ToLower(tag)] {
				score++
			}
		}
		if score == 0 {
			continue
		}
		at := candidate.CreatedAt
		if candidate.PublishedAt != nil {
			at = *candidate.PublishedAt
		}
		list = append(list, scored{candidate.Slug, score, at})
	}
	slices.SortStableFunc(list, func(a, b scored) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return b.at.Compare(a.at)
	})
	var out []string
	for i := 0; i < len(list) && i < limit; i++ {
		out = append(out, list[i].slug)
	}
	return out
}
