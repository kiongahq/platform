package editorial

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

// Document kinds owned by the editorial workspace.
const (
	PostKind      = "editorial_post"
	RevisionKind  = "blog_revision"
	MediaKind     = "blog_media"
	MemberKind    = "editorial_member"
	SessionKind   = "editorial_session"
	SettingsKind  = "editorial_settings"
	SlugKind      = "editorial_slug"
	LeaseKind     = "editorial_lease"
	MigrationKind = "editorial_migration"
)

// SessionTTL bounds an editorial session.
const SessionTTL = 4 * time.Hour

// PendingMediaTTL is how long an unattached upload survives.
const PendingMediaTTL = 24 * time.Hour

var (
	ErrNotMember        = errors.New("you are not a member of the editorial workspace")
	ErrSessionInvalid   = errors.New("editorial session expired or invalid")
	ErrBootstrapClosed  = errors.New("the editorial workspace already has an editorial admin")
	ErrLastAdmin        = errors.New("the workspace must keep at least one active editorial admin")
	ErrSlugTaken        = errors.New("another post already uses this slug")
	ErrMediaUnavailable = errors.New("an image in this post has not been uploaded")
)

// ConflictError is returned when an autosave was based on a stale revision.
type ConflictError struct{ Latest api.EditorialPost }

func (e ConflictError) Error() string {
	return fmt.Sprintf("post changed since revision you edited; latest is revision %d", e.Latest.Revision)
}

// Service implements the editorial workspace on the generic document store.
type Service struct {
	Docs  store.Documents
	Media MediaStore
	Now   func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func randomID(prefix string, size int) string {
	raw := make([]byte, size)
	_, _ = rand.Read(raw)
	return prefix + hex.EncodeToString(raw)
}

// --- members and bootstrap ---

func (s *Service) Member(subject string) (api.EditorialMember, error) {
	member, err := store.GetDoc[api.EditorialMember](s.Docs, MemberKind, subject)
	if errors.Is(err, store.ErrNotFound) {
		return member, ErrNotMember
	}
	return member, err
}

// ActiveMember returns the membership only when it exists and is enabled.
func (s *Service) ActiveMember(subject string) (api.EditorialMember, error) {
	member, err := s.Member(subject)
	if err != nil {
		return member, err
	}
	if member.Disabled || !ValidRole(member.Role) {
		return member, ErrNotMember
	}
	return member, nil
}

func (s *Service) Members() []api.EditorialMember {
	members := store.ListDocs[api.EditorialMember](s.Docs, MemberKind)
	sort.Slice(members, func(i, j int) bool { return members[i].Subject < members[j].Subject })
	return members
}

func (s *Service) activeAdmins() []string {
	var out []string
	for _, member := range s.Members() {
		if member.Role == api.EditorialAdmin && !member.Disabled {
			out = append(out, member.Subject)
		}
	}
	return out
}

// BootstrapAvailable reports whether the first editorial admin may be granted.
func (s *Service) BootstrapAvailable() bool { return len(s.activeAdmins()) == 0 }

// Bootstrap grants the first editorial_admin. The caller (handler) has
// already checked that the requester is the local bootstrap admin or an ML
// admin. A create-only claim document serializes concurrent attempts.
func (s *Service) Bootstrap(member api.EditorialMember, actor string) (api.EditorialMember, error) {
	if !s.BootstrapAvailable() {
		return api.EditorialMember{}, ErrBootstrapClosed
	}
	now := s.now()
	claimed := false
	_, err := store.UpdateDoc(s.Docs, SettingsKind, "bootstrap", func(current map[string]any, exists bool) (map[string]any, error) {
		if exists && current["subject"] != nil {
			return current, ErrBootstrapClosed
		}
		claimed = true
		return map[string]any{"subject": member.Subject, "by": actor, "at": now}, nil
	}, "editorial.bootstrap", actor)
	if err != nil {
		return api.EditorialMember{}, err
	}
	if !claimed {
		return api.EditorialMember{}, ErrBootstrapClosed
	}
	member.Role, member.Disabled, member.CreatedAt, member.UpdatedAt, member.CreatedBy = api.EditorialAdmin, false, now, now, actor
	if member.DisplayName == "" {
		member.DisplayName = member.Subject
	}
	return store.UpdateDoc(s.Docs, MemberKind, member.Subject, func(api.EditorialMember, bool) (api.EditorialMember, error) {
		return member, nil
	}, "editorial.member.granted", actor)
}

// PutMember creates or updates a membership; only editorial admins may.
func (s *Service) PutMember(admin api.EditorialMember, target api.EditorialMember) (api.EditorialMember, error) {
	if !AtLeast(admin.Role, api.EditorialAdmin) || admin.Disabled {
		return target, ErrForbidden
	}
	target.Subject = strings.TrimSpace(target.Subject)
	target.DisplayName = strings.TrimSpace(PlainText(target.DisplayName))
	target.Email = strings.TrimSpace(target.Email)
	if target.Subject == "" || len(target.Subject) > 200 || strings.ContainsAny(target.Subject, "/\x00") {
		return target, ValidationError{Message: "a subject (the person's platform identity) is required"}
	}
	if !ValidRole(target.Role) {
		return target, ValidationError{Message: "role must be author, editor or editorial_admin"}
	}
	if target.DisplayName == "" {
		target.DisplayName = target.Subject
	}
	if admins := s.activeAdmins(); slices.Contains(admins, target.Subject) && len(admins) == 1 && (target.Role != api.EditorialAdmin || target.Disabled) {
		return target, ErrLastAdmin
	}
	now := s.now()
	return store.UpdateDoc(s.Docs, MemberKind, target.Subject, func(current api.EditorialMember, exists bool) (api.EditorialMember, error) {
		if exists {
			target.CreatedAt, target.CreatedBy = current.CreatedAt, current.CreatedBy
		} else {
			target.CreatedAt, target.CreatedBy = now, admin.Subject
		}
		target.UpdatedAt = now
		return target, nil
	}, "editorial.member.updated", admin.Subject)
}

func (s *Service) DeleteMember(admin api.EditorialMember, subject string) error {
	if !AtLeast(admin.Role, api.EditorialAdmin) || admin.Disabled {
		return ErrForbidden
	}
	if admins := s.activeAdmins(); slices.Contains(admins, subject) && len(admins) == 1 {
		return ErrLastAdmin
	}
	return s.Docs.DeleteDocument(MemberKind, subject, "editorial.member.removed", admin.Subject)
}

// --- sessions ---

type session struct {
	ID        string    `json:"id"`
	Subject   string    `json:"subject"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func sessionID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSession mints a session for an active member. Only the token hash is
// stored.
func (s *Service) CreateSession(subject string) (string, time.Time, api.EditorialMember, error) {
	member, err := s.ActiveMember(subject)
	if err != nil {
		return "", time.Time{}, member, err
	}
	token := randomID("", 32)
	now := s.now()
	expires := now.Add(SessionTTL)
	_, err = store.UpdateDoc(s.Docs, SessionKind, sessionID(token), func(session, bool) (session, error) {
		return session{ID: sessionID(token), Subject: subject, CreatedAt: now, ExpiresAt: expires}, nil
	}, "editorial.session.created", subject)
	return token, expires, member, err
}

// ResolveSession returns the member behind a session token, re-checking the
// membership on every call so a removed or disabled member loses access
// immediately.
func (s *Service) ResolveSession(token string) (api.EditorialMember, error) {
	if token == "" {
		return api.EditorialMember{}, ErrSessionInvalid
	}
	value, err := store.GetDoc[session](s.Docs, SessionKind, sessionID(token))
	if err != nil || !value.ExpiresAt.After(s.now()) {
		return api.EditorialMember{}, ErrSessionInvalid
	}
	return s.ActiveMember(value.Subject)
}

func (s *Service) DeleteSession(token string, actor string) {
	if token != "" {
		_ = s.Docs.DeleteDocument(SessionKind, sessionID(token), "editorial.session.ended", actor)
	}
}

// --- posts ---

func (s *Service) Posts() []api.EditorialPost {
	posts := store.ListDocs[api.EditorialPost](s.Docs, PostKind)
	sort.SliceStable(posts, func(i, j int) bool { return posts[i].UpdatedAt.After(posts[j].UpdatedAt) })
	return posts
}

func (s *Service) Post(id string) (api.EditorialPost, error) {
	return store.GetDoc[api.EditorialPost](s.Docs, PostKind, id)
}

// PostBySlug resolves a slug through its claim document.
func (s *Service) PostBySlug(slug string) (api.EditorialPost, error) {
	claim, err := store.GetDoc[map[string]string](s.Docs, SlugKind, slug)
	if err == nil {
		if post, err := s.Post(claim["post_id"]); err == nil && post.Slug == slug {
			return post, nil
		}
	}
	for _, post := range s.Posts() {
		if post.Slug == slug {
			return post, nil
		}
	}
	return api.EditorialPost{}, store.ErrNotFound
}

type cleanedPost struct {
	title, summary, slug string
	tags                 []string
	blocks               []api.Block
	cover                *api.PostCover
	seo                  api.PostSEO
	version              string
}

func (s *Service) clean(request api.SavePostRequest, postID string) (cleanedPost, error) {
	var c cleanedPost
	c.title = strings.TrimSpace(PlainText(request.Title))
	c.summary = strings.TrimSpace(PlainText(request.Summary))
	if len(c.title) > MaxTitle || len(c.summary) > MaxSummary {
		return c, ValidationError{Message: fmt.Sprintf("title is limited to %d and summary to %d characters", MaxTitle, MaxSummary)}
	}
	c.slug = Slugify(request.Slug)
	if c.slug == "" {
		c.slug = Slugify(c.title)
	}
	if c.slug == "" {
		c.slug = "draft-" + strings.TrimPrefix(postID, "post-")
	}
	blocks, err := NormalizeBlocks(request.Blocks)
	if err != nil {
		return c, err
	}
	c.blocks, c.tags = blocks, NormalizeTags(request.Tags)
	c.seo = api.PostSEO{Title: truncate(strings.TrimSpace(PlainText(request.SEO.Title)), 120), Description: truncate(strings.TrimSpace(PlainText(request.SEO.Description)), 320)}
	if request.Cover != nil && request.Cover.MediaID != "" {
		if !mediaIDPattern.MatchString(request.Cover.MediaID) {
			return c, ValidationError{Message: "cover must reference an uploaded image"}
		}
		c.cover = &api.PostCover{MediaID: request.Cover.MediaID, Alt: strings.TrimSpace(PlainText(request.Cover.Alt))}
	}
	c.version = truncate(request.EditorVersion, 20)
	for _, id := range MediaIDs(c.blocks, c.cover) {
		if _, err := s.MediaItem(id); err != nil {
			return c, ErrMediaUnavailable
		}
	}
	return c, nil
}

func truncate(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

// claimSlug reserves slug for postID; it returns whether a new claim was made.
func (s *Service) claimSlug(slug, postID, actor string) (bool, error) {
	// Read outside the row lock: the file store's lock is not re-entrant.
	stale := ""
	if claim, err := store.GetDoc[map[string]string](s.Docs, SlugKind, slug); err == nil && claim["post_id"] != postID {
		if _, err := s.Post(claim["post_id"]); err == nil {
			return false, ErrSlugTaken
		}
		stale = claim["post_id"]
	}
	created := false
	_, err := store.UpdateDoc(s.Docs, SlugKind, slug, func(current map[string]string, exists bool) (map[string]string, error) {
		switch {
		case exists && current["post_id"] == postID:
			return current, store.ErrSkipWrite
		case exists && current["post_id"] != stale:
			return current, ErrSlugTaken
		}
		created = true
		return map[string]string{"post_id": postID}, nil
	}, "", actor)
	return created, err
}

func (s *Service) releaseSlug(slug, postID string) {
	if claim, err := store.GetDoc[map[string]string](s.Docs, SlugKind, slug); err == nil && claim["post_id"] == postID {
		_ = s.Docs.DeleteDocument(SlugKind, slug, "editorial.slug.released", "editorial")
	}
}

func revisionID(postID string, number int) string { return fmt.Sprintf("%s-r%d", postID, number) }

func (s *Service) writeRevision(post api.EditorialPost, reason string, restoredFrom int, actor string) {
	revision := api.PostRevision{
		ID: revisionID(post.ID, post.Revision), PostID: post.ID, Number: post.Revision,
		Title: post.Title, Summary: post.Summary, Slug: post.Slug, Tags: post.Tags, Blocks: post.Blocks,
		Cover: post.Cover, SEO: post.SEO, Reason: reason, RestoredFrom: restoredFrom, CreatedAt: post.UpdatedAt, CreatedBy: actor,
	}
	_, _ = store.UpdateDoc(s.Docs, RevisionKind, revision.ID, func(current api.PostRevision, exists bool) (api.PostRevision, error) {
		if exists {
			return current, store.ErrSkipWrite // revisions are immutable
		}
		return revision, nil
	}, "editorial.revision.created", actor)
}

func hashOf(c cleanedPost) string {
	return ContentHash(c.title, c.summary, c.slug, c.tags, c.blocks, c.cover, c.seo)
}

// CreatePost starts a draft owned by the member.
func (s *Service) CreatePost(member api.EditorialMember, request api.SavePostRequest) (api.EditorialPost, error) {
	if member.Disabled || !ValidRole(member.Role) {
		return api.EditorialPost{}, ErrForbidden
	}
	id := randomID("post-", 8)
	c, err := s.clean(request, id)
	if err != nil {
		return api.EditorialPost{}, err
	}
	if _, err := s.claimSlug(c.slug, id, member.Subject); err != nil {
		return api.EditorialPost{}, err
	}
	now := s.now()
	post := api.EditorialPost{
		ID: id, Slug: c.slug, Title: c.title, Summary: c.summary, Blocks: c.blocks, EditorVersion: c.version, Cover: c.cover,
		Tags: c.tags, SEO: c.seo, Author: member.DisplayName, AuthorSubject: member.Subject, Status: api.PostDraft,
		Revision: 1, ContentHash: hashOf(c), MediaIDs: MediaIDs(c.blocks, c.cover), CreatedAt: now, UpdatedAt: now, UpdatedBy: member.Subject,
	}
	if post.Author == "" {
		post.Author = member.Subject
	}
	saved, err := store.UpdateDoc(s.Docs, PostKind, id, func(api.EditorialPost, bool) (api.EditorialPost, error) { return post, nil }, "editorial.post.created", member.Subject)
	if err != nil {
		s.releaseSlug(c.slug, id)
		return saved, err
	}
	s.writeRevision(saved, "create", 0, member.Subject)
	s.attach(saved.MediaIDs, saved.ID)
	return saved, nil
}

// SavePost is the autosave/save endpoint. A stale base revision returns
// ConflictError with the latest post; an unchanged body is a no-op that
// returns the current post without a new revision.
func (s *Service) SavePost(member api.EditorialMember, id string, request api.SavePostRequest) (api.EditorialPost, bool, error) {
	current, err := s.Post(id)
	if err != nil {
		return current, false, err
	}
	if err := CanEdit(member, current); err != nil {
		return current, false, err
	}
	c, err := s.clean(request, id)
	if err != nil {
		return current, false, err
	}
	newClaim, err := s.claimSlug(c.slug, id, member.Subject)
	if err != nil {
		return current, false, err
	}
	hash := hashOf(c)
	now := s.now()
	wrote := false
	var oldSlug string
	saved, err := store.UpdateDoc(s.Docs, PostKind, id, func(post api.EditorialPost, exists bool) (api.EditorialPost, error) {
		if !exists {
			return post, store.ErrNotFound
		}
		if err := CanEdit(member, post); err != nil {
			return post, err
		}
		if post.Revision != request.BaseRevision {
			return post, ConflictError{Latest: post}
		}
		if post.ContentHash == hash {
			return post, store.ErrSkipWrite
		}
		oldSlug = post.Slug
		post.Title, post.Summary, post.Slug, post.Tags, post.Blocks = c.title, c.summary, c.slug, c.tags, c.blocks
		post.Cover, post.SEO, post.ContentHash, post.MediaIDs = c.cover, c.seo, hash, MediaIDs(c.blocks, c.cover)
		if c.version != "" {
			post.EditorVersion = c.version
		}
		post.Revision++
		post.UpdatedAt, post.UpdatedBy = now, member.Subject
		wrote = true
		return post, nil
	}, "editorial.post.saved", member.Subject)
	if err != nil {
		if newClaim {
			s.releaseSlug(c.slug, id)
		}
		return saved, false, err
	}
	if wrote {
		s.writeRevision(saved, "save", 0, member.Subject)
		s.attach(saved.MediaIDs, saved.ID)
		if oldSlug != "" && oldSlug != saved.Slug {
			s.releaseSlug(oldSlug, id)
		}
	}
	return saved, wrote, nil
}

func (s *Service) coverAlt(post api.EditorialPost) string {
	if post.Cover == nil {
		return ""
	}
	if post.Cover.Alt != "" {
		return post.Cover.Alt
	}
	if media, err := s.MediaItem(post.Cover.MediaID); err == nil {
		return media.Alt
	}
	return ""
}

// Transition applies a workflow action.
func (s *Service) Transition(member api.EditorialMember, id string, request api.TransitionRequest) (api.EditorialPost, error) {
	current, err := s.Post(id)
	if err != nil {
		return current, err
	}
	coverAlt := s.coverAlt(current)
	now := s.now()
	return store.UpdateDoc(s.Docs, PostKind, id, func(post api.EditorialPost, exists bool) (api.EditorialPost, error) {
		if !exists {
			return post, store.ErrNotFound
		}
		return Apply(member, post, request, coverAlt, now)
	}, "editorial.post."+request.Action, member.Subject)
}

func (s *Service) Revisions(postID string) []api.PostRevision {
	var out []api.PostRevision
	for _, revision := range store.ListDocs[api.PostRevision](s.Docs, RevisionKind) {
		if revision.PostID == postID {
			out = append(out, revision)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	return out
}

func (s *Service) Revision(postID string, number int) (api.PostRevision, error) {
	return store.GetDoc[api.PostRevision](s.Docs, RevisionKind, revisionID(postID, number))
}

// Restore copies an old revision's content into a new revision.
func (s *Service) Restore(member api.EditorialMember, postID string, number, base int) (api.EditorialPost, error) {
	revision, err := s.Revision(postID, number)
	if err != nil {
		return api.EditorialPost{}, err
	}
	current, err := s.Post(postID)
	if err != nil {
		return current, err
	}
	// Restoring content never moves the post's public URL: the slug stays.
	saved, wrote, err := s.SavePost(member, postID, api.SavePostRequest{
		BaseRevision: base, Title: revision.Title, Summary: revision.Summary, Slug: current.Slug, Tags: revision.Tags,
		Blocks: revision.Blocks, Cover: revision.Cover, SEO: revision.SEO,
	})
	if err != nil || !wrote {
		return saved, err
	}
	// Re-label the revision SavePost just wrote: it is a restore.
	_, _ = store.UpdateDoc(s.Docs, RevisionKind, revisionID(postID, saved.Revision), func(current api.PostRevision, exists bool) (api.PostRevision, error) {
		if !exists {
			return current, store.ErrSkipWrite
		}
		current.Reason, current.RestoredFrom = "restore", number
		return current, nil
	}, "editorial.revision.restored", member.Subject)
	return saved, nil
}

// --- media ---

func (s *Service) MediaItem(id string) (api.BlogMedia, error) {
	return store.GetDoc[api.BlogMedia](s.Docs, MediaKind, id)
}

func (s *Service) MediaItems() []api.BlogMedia {
	return store.ListDocs[api.BlogMedia](s.Docs, MediaKind)
}

// Upload processes an image and stores its variants as a pending media item.
func (s *Service) Upload(ctx context.Context, member api.EditorialMember, data []byte, alt, caption, attribution string) (api.BlogMedia, error) {
	if member.Disabled || !ValidRole(member.Role) {
		return api.BlogMedia{}, ErrForbidden
	}
	processed, err := ProcessImage(data)
	if err != nil {
		return api.BlogMedia{}, err
	}
	media := api.BlogMedia{
		ID: randomID("med-", 8), Alt: truncate(strings.TrimSpace(PlainText(alt)), 300), Caption: truncate(CleanInline(caption), 600),
		Attribution: truncate(strings.TrimSpace(PlainText(attribution)), 200), Width: processed.Width, Height: processed.Height,
		Status: "pending", CreatedAt: s.now(), Uploader: member.Subject, Backend: s.Media.Name(),
	}
	for _, variant := range processed.Variants {
		key := media.ID + "/" + variant.Name
		if err := s.Media.Put(ctx, key, variant.ContentType, variant.Data); err != nil {
			s.deleteObjects(ctx, media)
			return api.BlogMedia{}, fmt.Errorf("store image: %w", err)
		}
		media.Variants = append(media.Variants, api.MediaVariant{Name: variant.Name, Width: variant.Width, Height: variant.Height, Key: key, ContentType: variant.ContentType, Bytes: len(variant.Data)})
	}
	saved, err := store.UpdateDoc(s.Docs, MediaKind, media.ID, func(api.BlogMedia, bool) (api.BlogMedia, error) { return media, nil }, "editorial.media.uploaded", member.Subject)
	if err != nil {
		s.deleteObjects(ctx, media)
	}
	return saved, err
}

// UpdateMedia edits descriptive fields (alt, caption, attribution).
func (s *Service) UpdateMedia(member api.EditorialMember, id string, alt, caption, attribution *string) (api.BlogMedia, error) {
	if member.Disabled || !ValidRole(member.Role) {
		return api.BlogMedia{}, ErrForbidden
	}
	return store.UpdateDoc(s.Docs, MediaKind, id, func(media api.BlogMedia, exists bool) (api.BlogMedia, error) {
		if !exists {
			return media, store.ErrNotFound
		}
		if alt != nil {
			media.Alt = truncate(strings.TrimSpace(PlainText(*alt)), 300)
		}
		if caption != nil {
			media.Caption = truncate(CleanInline(*caption), 600)
		}
		if attribution != nil {
			media.Attribution = truncate(strings.TrimSpace(PlainText(*attribution)), 200)
		}
		return media, nil
	}, "editorial.media.updated", member.Subject)
}

func (s *Service) attach(ids []string, postID string) {
	for _, id := range ids {
		_, _ = store.UpdateDoc(s.Docs, MediaKind, id, func(media api.BlogMedia, exists bool) (api.BlogMedia, error) {
			if !exists || (media.Status == "attached" && slices.Contains(media.PostIDs, postID)) {
				return media, store.ErrSkipWrite
			}
			media.Status = "attached"
			if !slices.Contains(media.PostIDs, postID) {
				media.PostIDs = append(media.PostIDs, postID)
			}
			return media, nil
		}, "editorial.media.attached", "editorial")
	}
}

// MediaPublic reports whether a media item is attached to a published post.
func (s *Service) MediaPublic(media api.BlogMedia) bool {
	if media.Status != "attached" {
		return false
	}
	for _, postID := range media.PostIDs {
		if post, err := s.Post(postID); err == nil && post.Status == api.PostPublished && slices.Contains(post.MediaIDs, media.ID) {
			return true
		}
	}
	return false
}

func (s *Service) deleteObjects(ctx context.Context, media api.BlogMedia) {
	for _, name := range []string{"480", "960", "1600"} {
		_ = s.Media.Delete(ctx, media.ID+"/"+name)
	}
}

// Sweep deletes pending uploads older than PendingMediaTTL and expired
// sessions. It returns how many media items were removed.
func (s *Service) Sweep(ctx context.Context) int {
	now := s.now()
	removed := 0
	for _, media := range s.MediaItems() {
		if media.Status != "pending" || now.Sub(media.CreatedAt) < PendingMediaTTL {
			continue
		}
		s.deleteObjects(ctx, media)
		if s.Docs.DeleteDocument(MediaKind, media.ID, "editorial.media.swept", "editorial-sweeper") == nil {
			removed++
		}
	}
	for _, value := range store.ListDocs[session](s.Docs, SessionKind) {
		if value.ID != "" && !value.ExpiresAt.After(now) {
			_ = s.Docs.DeleteDocument(SessionKind, value.ID, "editorial.session.expired", "editorial-sweeper")
		}
	}
	return removed
}

// --- scheduled publishing ---

type lease struct {
	Holder    string    `json:"holder"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Publisher fires scheduled posts and sweeps stale media. Like the pipeline
// scheduler it holds a lease document so one replica does the work; the
// per-post row lock plus PublishDue's status check make double publication
// impossible even without the lease.
type Publisher struct {
	Service  *Service
	Holder   string
	LeaseTTL time.Duration
	Interval time.Duration
}

var errNotLeader = errors.New("another replica holds the editorial lease")

// Tick publishes every due post and returns the ids it published.
func (p *Publisher) Tick(ctx context.Context) ([]string, error) {
	ttl := p.LeaseTTL
	if ttl <= 0 {
		ttl = 45 * time.Second
	}
	now := p.Service.now()
	_, err := store.UpdateDoc(p.Service.Docs, LeaseKind, "publisher", func(current lease, exists bool) (lease, error) {
		if exists && current.Holder != p.Holder && current.ExpiresAt.After(now) {
			return current, errNotLeader
		}
		return lease{Holder: p.Holder, ExpiresAt: now.Add(ttl)}, nil
	}, "", "editorial-scheduler")
	if err != nil {
		return nil, err
	}
	var published []string
	for _, post := range p.Service.Posts() {
		if _, due := PublishDue(post, now); !due {
			continue
		}
		fired := false
		_, err := store.UpdateDoc(p.Service.Docs, PostKind, post.ID, func(current api.EditorialPost, exists bool) (api.EditorialPost, error) {
			next, due := PublishDue(current, now)
			if !exists || !due {
				return current, store.ErrSkipWrite
			}
			fired = true
			return next, nil
		}, "editorial.post.published_scheduled", "editorial-scheduler")
		if err == nil && fired {
			published = append(published, post.ID)
		}
	}
	p.Service.Sweep(ctx)
	return published, nil
}

// Run ticks until ctx ends.
func (p *Publisher) Run(ctx context.Context, report func([]string, error)) {
	interval := p.Interval
	if interval <= 0 {
		interval = 20 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		published, err := p.Tick(ctx)
		if errors.Is(err, errNotLeader) {
			err = nil
		}
		if report != nil {
			report(published, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
