package api

import (
	"encoding/json"
	"time"
)

// Editorial workflow statuses. A post moves draft -> in_review ->
// scheduled|published; unpublish returns it to draft; archive hides it.
const (
	PostDraft     = "draft"
	PostInReview  = "in_review"
	PostScheduled = "scheduled"
	PostPublished = "published"
	PostArchived  = "archived"
)

// Editorial roles, least to most privileged. They are separate from the ML
// platform roles: an ML administrator has none of them unless granted.
const (
	EditorialAuthor = "author"
	EditorialEditor = "editor"
	EditorialAdmin  = "editorial_admin"
)

// Block is one Editor.js block. Data stays raw JSON so the canonical content
// round-trips exactly; the editorial package validates it per block type.
type Block struct {
	ID    string          `json:"id,omitempty"`
	Type  string          `json:"type"`
	Data  json.RawMessage `json:"data"`
	Tunes json.RawMessage `json:"tunes,omitempty"`
}

// PostCover references an uploaded image used as the post's cover/social card.
type PostCover struct {
	MediaID string `json:"media_id"`
	Alt     string `json:"alt"`
}

type PostSEO struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// EditorialPost is the canonical post document (kind editorial_post).
type EditorialPost struct {
	ID             string     `json:"id"`
	Slug           string     `json:"slug"`
	Title          string     `json:"title"`
	Summary        string     `json:"summary"`
	Blocks         []Block    `json:"blocks"`
	EditorVersion  string     `json:"editor_version,omitempty"`
	Cover          *PostCover `json:"cover,omitempty"`
	Tags           []string   `json:"tags"`
	SEO            PostSEO    `json:"seo"`
	Author         string     `json:"author"`
	AuthorSubject  string     `json:"author_subject,omitempty"`
	Status         string     `json:"status"`
	Revision       int        `json:"revision"`
	ContentHash    string     `json:"content_hash,omitempty"`
	MediaIDs       []string   `json:"media_ids,omitempty"`
	PublishAt      *time.Time `json:"publish_at,omitempty"`
	PublishedAt    *time.Time `json:"published_at,omitempty"`
	SubmittedAt    *time.Time `json:"submitted_at,omitempty"`
	ArchivedAt     *time.Time `json:"archived_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	UpdatedBy      string     `json:"updated_by,omitempty"`
	LegacyMarkdown string     `json:"legacy_markdown,omitempty"`
	MigratedFrom   string     `json:"migrated_from,omitempty"`
}

// PostRevision is an immutable snapshot written on every content save
// (kind blog_revision, id "<post id>-r<number>").
type PostRevision struct {
	ID           string     `json:"id"`
	PostID       string     `json:"post_id"`
	Number       int        `json:"number"`
	Title        string     `json:"title"`
	Summary      string     `json:"summary"`
	Slug         string     `json:"slug"`
	Tags         []string   `json:"tags"`
	Blocks       []Block    `json:"blocks"`
	Cover        *PostCover `json:"cover,omitempty"`
	SEO          PostSEO    `json:"seo"`
	Reason       string     `json:"reason"`
	RestoredFrom int        `json:"restored_from,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	CreatedBy    string     `json:"created_by"`
}

// SavePostRequest is the body of create and autosave. BaseRevision must equal
// the post's current revision on update or the save returns 409.
type SavePostRequest struct {
	BaseRevision  int        `json:"base_revision"`
	Title         string     `json:"title"`
	Summary       string     `json:"summary"`
	Slug          string     `json:"slug"`
	Tags          []string   `json:"tags"`
	Blocks        []Block    `json:"blocks"`
	EditorVersion string     `json:"editor_version,omitempty"`
	Cover         *PostCover `json:"cover,omitempty"`
	SEO           PostSEO    `json:"seo"`
	PublishAt     *time.Time `json:"publish_at,omitempty"`
}

// TransitionRequest moves a post through the workflow.
type TransitionRequest struct {
	Action    string     `json:"action"`
	PublishAt *time.Time `json:"publish_at,omitempty"`
}

type MediaVariant struct {
	Name        string `json:"name"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
	Bytes       int    `json:"bytes"`
}

// BlogMedia is one uploaded image (kind blog_media).
type BlogMedia struct {
	ID          string         `json:"id"`
	Alt         string         `json:"alt"`
	Caption     string         `json:"caption,omitempty"`
	Attribution string         `json:"attribution,omitempty"`
	Width       int            `json:"width"`
	Height      int            `json:"height"`
	Variants    []MediaVariant `json:"variants"`
	Status      string         `json:"status"`
	PostIDs     []string       `json:"post_ids,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	Uploader    string         `json:"uploader"`
	Backend     string         `json:"backend"`
}

// EditorialMember grants one verified identity an editorial role
// (kind editorial_member, id = subject).
type EditorialMember struct {
	Subject     string    `json:"subject"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email,omitempty"`
	Role        string    `json:"role"`
	Disabled    bool      `json:"disabled"`
	CreatedAt   time.Time `json:"created_at"`
	CreatedBy   string    `json:"created_by"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PublicImage is the reader-facing projection of a media document.
type PublicImage struct {
	URL    string `json:"url"`
	SrcSet string `json:"srcset"`
	Alt    string `json:"alt"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// PublicBlogPost keeps the original /api/v1/blogs fields and adds the block
// content, the server-rendered HTML and the cover image.
type PublicBlogPost struct {
	ID             string       `json:"id"`
	Slug           string       `json:"slug"`
	Title          string       `json:"title"`
	Summary        string       `json:"summary"`
	Content        string       `json:"content"`
	Author         string       `json:"author"`
	Tags           []string     `json:"tags"`
	Status         string       `json:"status"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	PublishedAt    *time.Time   `json:"published_at,omitempty"`
	ReadingMinutes int          `json:"reading_minutes"`
	SEO            PostSEO      `json:"seo"`
	Cover          *PublicImage `json:"cover,omitempty"`
	Blocks         []Block      `json:"blocks,omitempty"`
	RenderedHTML   string       `json:"rendered_html,omitempty"`
	Related        []string     `json:"related,omitempty"`
}
