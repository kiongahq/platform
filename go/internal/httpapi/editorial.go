package httpapi

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/editorial"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
	"github.com/prometheus/client_golang/prometheus"
)

// EditorialCookie carries the editorial session. It is distinct from the
// platform session cookies so ML-console access never implies publishing
// rights; SameSite=Strict keeps it off every cross-site request.
const EditorialCookie = "kionga_editorial"

var (
	editorialSaves = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "kionga", Subsystem: "editorial", Name: "saves_total", Help: "Editorial post saves by result (saved, unchanged, conflict, rejected).",
	}, []string{"result"})
	editorialTransitions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "kionga", Subsystem: "editorial", Name: "transitions_total", Help: "Editorial workflow actions by action and result.",
	}, []string{"action", "result"})
	editorialUploads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "kionga", Subsystem: "editorial", Name: "media_uploads_total", Help: "Blog media uploads by result.",
	}, []string{"result"})
	editorialScheduled = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "kionga", Subsystem: "editorial", Name: "scheduled_publishes_total", Help: "Posts published by the scheduled publisher.",
	})
)

func init() {
	httpRegistry.MustRegister(editorialSaves, editorialTransitions, editorialUploads, editorialScheduled)
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		e := &editorialAPI{server: s, svc: &editorial.Service{Docs: s.store, Media: editorial.MediaStoreFromEnv()}}
		if migrated, err := e.svc.MigrateLegacy(s.store.BlogPosts()); err != nil {
			log.Printf("editorial migration: %v", err)
		} else if migrated > 0 {
			log.Printf("editorial migration: converted %d Markdown posts to blocks", migrated)
		}
		mux.HandleFunc("GET /api/v1/blogs", e.publicPosts)
		mux.HandleFunc("GET /api/v1/blogs/{slug}", e.publicPost)
		mux.HandleFunc("GET /media/blog/{id}/{variant}", e.serveMedia)

		mux.HandleFunc("GET /api/v1/editorial/bootstrap", e.bootstrapStatus)
		mux.HandleFunc("POST /api/v1/editorial/bootstrap", e.bootstrap)
		mux.HandleFunc("POST /api/v1/editorial/session", e.createSession)
		mux.HandleFunc("GET /api/v1/editorial/session", e.member(e.currentSession))
		mux.HandleFunc("DELETE /api/v1/editorial/session", e.endSession)

		mux.HandleFunc("GET /api/v1/editorial/posts", e.member(e.listPosts))
		mux.HandleFunc("POST /api/v1/editorial/posts", e.member(e.createPost))
		mux.HandleFunc("GET /api/v1/editorial/posts/{id}", e.member(e.getPost))
		mux.HandleFunc("PUT /api/v1/editorial/posts/{id}", e.member(e.savePost))
		mux.HandleFunc("POST /api/v1/editorial/posts/{id}/transition", e.member(e.transition))
		mux.HandleFunc("GET /api/v1/editorial/posts/{id}/revisions", e.member(e.revisions))
		mux.HandleFunc("GET /api/v1/editorial/posts/{id}/revisions/{number}", e.member(e.revision))
		mux.HandleFunc("POST /api/v1/editorial/posts/{id}/revisions/{number}/restore", e.member(e.restore))
		mux.HandleFunc("GET /api/v1/editorial/posts/{id}/compare", e.member(e.compare))
		mux.HandleFunc("GET /api/v1/editorial/posts/{id}/markdown", e.member(e.exportMarkdown))
		mux.HandleFunc("POST /api/v1/editorial/markdown", e.member(e.importMarkdown))
		mux.HandleFunc("POST /api/v1/editorial/preview", e.member(e.preview))

		mux.HandleFunc("GET /api/v1/editorial/media", e.member(e.listMedia))
		mux.HandleFunc("POST /api/v1/editorial/media", e.member(e.upload))
		mux.HandleFunc("PATCH /api/v1/editorial/media/{id}", e.member(e.updateMedia))

		mux.HandleFunc("GET /api/v1/editorial/members", e.member(e.listMembers))
		mux.HandleFunc("PUT /api/v1/editorial/members/{subject}", e.member(e.putMember))
		mux.HandleFunc("DELETE /api/v1/editorial/members/{subject}", e.member(e.deleteMember))
		mux.HandleFunc("GET /api/v1/editorial/settings", e.member(e.getSettings))
		mux.HandleFunc("PUT /api/v1/editorial/settings", e.member(e.putSettings))
	})
}

type editorialAPI struct {
	server *Server
	svc    *editorial.Service
}

// StartEditorialPublisher runs the scheduled publisher and media sweeper
// until ctx ends. Every replica may run it; a lease keeps one active.
func StartEditorialPublisher(ctx context.Context, data store.Repository) {
	if os.Getenv("KIONGA_SCHEDULER") == "off" {
		return
	}
	host, _ := os.Hostname()
	publisher := &editorial.Publisher{
		Service: &editorial.Service{Docs: data, Media: editorial.MediaStoreFromEnv()},
		Holder:  host + "-editorial-" + time.Now().UTC().Format("150405.000"),
	}
	go publisher.Run(ctx, func(published []string, err error) {
		if err != nil {
			log.Printf("editorial publisher tick failed: %v", err)
		}
		for _, id := range published {
			editorialScheduled.Inc()
			log.Printf("editorial publisher: published scheduled post %s", id)
		}
	})
	log.Printf("editorial publisher started (holder %s, media backend %s)", publisher.Holder, publisher.Service.Media.Name())
}

type memberHandler func(http.ResponseWriter, *http.Request, api.EditorialMember)

func sessionToken(r *http.Request) string {
	if cookie, err := r.Cookie(EditorialCookie); err == nil {
		return cookie.Value
	}
	return ""
}

// member enforces the editorial session on every request: the cookie must
// resolve to a live session, the membership is re-read (so removal or
// suspension applies immediately), and when a platform identity is present
// it must be the same subject that opened the session.
func (e *editorialAPI) member(next memberHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, http.StatusForbidden, "cross_site", "Editorial changes must come from the editorial workspace.")
			return
		}
		member, err := e.svc.ResolveSession(sessionToken(r))
		if err != nil {
			writeError(w, http.StatusForbidden, "editorial_session_required", "Open the editorial workspace with an editorial membership to continue.")
			return
		}
		if value, ok := auth.PrincipalFrom(r.Context()); ok && value.Subject != "" && value.Subject != member.Subject && !slices.Contains(value.Roles, auth.RoleService) {
			writeError(w, http.StatusForbidden, "editorial_session_mismatch", "This editorial session belongs to a different signed-in identity.")
			return
		}
		next(w, r, member)
	}
}

func secureCookies(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" || os.Getenv("OIDC_ISSUER") != ""
}

func (e *editorialAPI) setCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if token == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{Name: EditorialCookie, Value: token, Path: "/", Expires: expires, MaxAge: maxAge,
		HttpOnly: true, Secure: secureCookies(r), SameSite: http.SameSiteStrictMode})
}

// canBootstrap: the local bootstrap administrator, or an ML admin (audited).
func canBootstrap(value auth.Principal) bool {
	if slices.Contains(value.Roles, auth.RoleAdmin) {
		return true
	}
	bootstrap := os.Getenv("MLAIOPS_LOCAL_USERNAME")
	if bootstrap == "" {
		bootstrap = "admin"
	}
	return os.Getenv("OIDC_ISSUER") == "" && value.Subject == bootstrap
}

func (e *editorialAPI) bootstrapStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"available": e.svc.BootstrapAvailable(), "allowed": canBootstrap(principal(r))})
}

func (e *editorialAPI) bootstrap(w http.ResponseWriter, r *http.Request) {
	value := principal(r)
	if !canBootstrap(value) || value.Subject == "" {
		writeError(w, http.StatusForbidden, "forbidden", "Only the bootstrap administrator or an ML administrator can open the editorial workspace for the first time.")
		return
	}
	var body struct {
		DisplayName string `json:"display_name"`
	}
	_ = decode(r, &body)
	member, err := e.svc.Bootstrap(api.EditorialMember{Subject: value.Subject, Email: value.Email, DisplayName: body.DisplayName}, actor(r))
	if err != nil {
		e.writeErr(w, err)
		return
	}
	token, expires, _, err := e.svc.CreateSession(member.Subject)
	if err != nil {
		e.writeErr(w, err)
		return
	}
	e.setCookie(w, r, token, expires)
	writeJSON(w, http.StatusCreated, map[string]any{"member": member, "expires_at": expires})
}

func (e *editorialAPI) createSession(w http.ResponseWriter, r *http.Request) {
	value := principal(r)
	if value.Subject == "" || slices.Contains(value.Roles, auth.RoleService) || value.Credential == "api_token" {
		writeError(w, http.StatusForbidden, "not_member", "Sign in with your own identity to open the editorial workspace.")
		return
	}
	token, expires, member, err := e.svc.CreateSession(value.Subject)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "not_member", "message": "Your account is not a member of the editorial workspace. Ask an editorial admin to add you.",
			"bootstrap_available": e.svc.BootstrapAvailable(), "can_bootstrap": canBootstrap(value),
		})
		return
	}
	e.setCookie(w, r, token, expires)
	writeJSON(w, http.StatusCreated, map[string]any{"member": member, "expires_at": expires})
}

func (e *editorialAPI) currentSession(w http.ResponseWriter, _ *http.Request, member api.EditorialMember) {
	writeJSON(w, http.StatusOK, map[string]any{"member": member, "media_backend": e.svc.Media.Name(), "limits": map[string]any{
		"max_upload_bytes": editorial.MaxUploadBytes, "variant_widths": editorial.VariantWidths, "session_hours": int(editorial.SessionTTL.Hours()),
	}})
}

func (e *editorialAPI) endSession(w http.ResponseWriter, r *http.Request) {
	e.svc.DeleteSession(sessionToken(r), actor(r))
	e.setCookie(w, r, "", time.Unix(1, 0))
	w.WriteHeader(http.StatusNoContent)
}

func (e *editorialAPI) writeErr(w http.ResponseWriter, err error) {
	var validation editorial.ValidationError
	var conflict editorial.ConflictError
	switch {
	case errors.As(err, &conflict):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "revision_conflict", "message": err.Error(), "latest": conflict.Latest})
	case errors.As(err, &validation):
		writeError(w, http.StatusUnprocessableEntity, "validation_error", validation.Message)
	case errors.Is(err, editorial.ErrForbidden), errors.Is(err, editorial.ErrNotMember):
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, editorial.ErrTransition), errors.Is(err, editorial.ErrSlugTaken), errors.Is(err, editorial.ErrBootstrapClosed), errors.Is(err, editorial.ErrLastAdmin):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, editorial.ErrMediaUnavailable):
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	default:
		log.Printf("editorial: %v", err)
		writeError(w, http.StatusInternalServerError, "internal", "The editorial service could not complete this request.")
	}
}

// postView adds what the editor UI needs to decide which controls to show.
type postView struct {
	api.EditorialPost
	Actions []string `json:"actions"`
	CanEdit bool     `json:"can_edit"`
}

func view(member api.EditorialMember, post api.EditorialPost) postView {
	actions := editorial.Actions(member, post)
	if actions == nil {
		actions = []string{}
	}
	return postView{EditorialPost: post, Actions: actions, CanEdit: editorial.CanEdit(member, post) == nil}
}

func (e *editorialAPI) listPosts(w http.ResponseWriter, r *http.Request, member api.EditorialMember) {
	status := r.URL.Query().Get("status")
	mine := r.URL.Query().Get("mine") == "1"
	items := []postView{}
	for _, post := range e.svc.Posts() {
		if !editorial.CanView(member, post) || (status != "" && post.Status != status) || (mine && post.AuthorSubject != member.Subject) {
			continue
		}
		post.Blocks, post.LegacyMarkdown = nil, ""
		items = append(items, view(member, post))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (e *editorialAPI) createPost(w http.ResponseWriter, r *http.Request, member api.EditorialMember) {
	var request api.SavePostRequest
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	post, err := e.svc.CreatePost(member, request)
	if err != nil {
		editorialSaves.WithLabelValues("rejected").Inc()
		e.writeErr(w, err)
		return
	}
	editorialSaves.WithLabelValues("saved").Inc()
	writeJSON(w, http.StatusCreated, view(member, post))
}

func (e *editorialAPI) visiblePost(w http.ResponseWriter, r *http.Request, member api.EditorialMember) (api.EditorialPost, bool) {
	post, err := e.svc.Post(r.PathValue("id"))
	if err != nil || !editorial.CanView(member, post) {
		writeError(w, http.StatusNotFound, "not_found", "post not found")
		return post, false
	}
	return post, true
}

func (e *editorialAPI) getPost(w http.ResponseWriter, r *http.Request, member api.EditorialMember) {
	if post, ok := e.visiblePost(w, r, member); ok {
		writeJSON(w, http.StatusOK, view(member, post))
	}
}

func (e *editorialAPI) savePost(w http.ResponseWriter, r *http.Request, member api.EditorialMember) {
	if _, ok := e.visiblePost(w, r, member); !ok {
		return
	}
	var request api.SavePostRequest
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	post, wrote, err := e.svc.SavePost(member, r.PathValue("id"), request)
	if err != nil {
		var conflict editorial.ConflictError
		if errors.As(err, &conflict) {
			editorialSaves.WithLabelValues("conflict").Inc()
		} else {
			editorialSaves.WithLabelValues("rejected").Inc()
		}
		e.writeErr(w, err)
		return
	}
	if wrote {
		editorialSaves.WithLabelValues("saved").Inc()
	} else {
		editorialSaves.WithLabelValues("unchanged").Inc()
	}
	writeJSON(w, http.StatusOK, view(member, post))
}

func (e *editorialAPI) transition(w http.ResponseWriter, r *http.Request, member api.EditorialMember) {
	if _, ok := e.visiblePost(w, r, member); !ok {
		return
	}
	var request api.TransitionRequest
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	post, err := e.svc.Transition(member, r.PathValue("id"), request)
	if err != nil {
		editorialTransitions.WithLabelValues(boundedAction(request.Action), "rejected").Inc()
		e.writeErr(w, err)
		return
	}
	editorialTransitions.WithLabelValues(boundedAction(request.Action), "ok").Inc()
	writeJSON(w, http.StatusOK, view(member, post))
}

func boundedAction(action string) string {
	switch action {
	case editorial.ActionSubmit, editorial.ActionWithdraw, editorial.ActionRequestChanges, editorial.ActionPublish,
		editorial.ActionSchedule, editorial.ActionUnpublish, editorial.ActionArchive, editorial.ActionRestore:
		return action
	}
	return "unknown"
}

func (e *editorialAPI) revisions(w http.ResponseWriter, r *http.Request, member api.EditorialMember) {
	post, ok := e.visiblePost(w, r, member)
	if !ok {
		return
	}
	items := []map[string]any{}
	for _, revision := range e.svc.Revisions(post.ID) {
		items = append(items, map[string]any{"number": revision.Number, "reason": revision.Reason, "restored_from": revision.RestoredFrom,
			"title": revision.Title, "created_at": revision.CreatedAt, "created_by": revision.CreatedBy, "blocks": len(revision.Blocks)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "current": post.Revision})
}

func pathNumber(r *http.Request, name string) (int, bool) {
	value, err := strconv.Atoi(r.PathValue(name))
	return value, err == nil && value > 0
}

func (e *editorialAPI) revision(w http.ResponseWriter, r *http.Request, member api.EditorialMember) {
	post, ok := e.visiblePost(w, r, member)
	if !ok {
		return
	}
	number, ok := pathNumber(r, "number")
	revision, err := e.svc.Revision(post.ID, number)
	if !ok || err != nil {
		writeError(w, http.StatusNotFound, "not_found", "revision not found")
		return
	}
	writeJSON(w, http.StatusOK, revision)
}

func (e *editorialAPI) restore(w http.ResponseWriter, r *http.Request, member api.EditorialMember) {
	post, ok := e.visiblePost(w, r, member)
	if !ok {
		return
	}
	number, ok := pathNumber(r, "number")
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "revision not found")
		return
	}
	var body struct {
		BaseRevision int `json:"base_revision"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	restored, err := e.svc.Restore(member, post.ID, number, body.BaseRevision)
	if err != nil {
		e.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view(member, restored))
}

func (e *editorialAPI) compare(w http.ResponseWriter, r *http.Request, member api.EditorialMember) {
	post, ok := e.visiblePost(w, r, member)
	if !ok {
		return
	}
	from, errFrom := strconv.Atoi(r.URL.Query().Get("from"))
	to, errTo := strconv.Atoi(r.URL.Query().Get("to"))
	if errTo != nil || to == 0 {
		to = post.Revision
	}
	before, err1 := e.svc.Revision(post.ID, from)
	after, err2 := e.svc.Revision(post.ID, to)
	if errFrom != nil || err1 != nil || err2 != nil {
		writeError(w, http.StatusNotFound, "not_found", "revision not found")
		return
	}
	writeJSON(w, http.StatusOK, editorial.Compare(before, after))
}

func (e *editorialAPI) exportMarkdown(w http.ResponseWriter, r *http.Request, member api.EditorialMember) {
	post, ok := e.visiblePost(w, r, member)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+post.Slug+`.md"`)
	_, _ = io.WriteString(w, "# "+post.Title+"\n\n"+editorial.ToMarkdown(post.Blocks))
}

func (e *editorialAPI) importMarkdown(w http.ResponseWriter, r *http.Request, _ api.EditorialMember) {
	var body struct {
		Markdown string `json:"markdown"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	blocks, err := editorial.NormalizeBlocks(editorial.FromMarkdown(body.Markdown))
	if err != nil {
		e.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blocks": blocks})
}

func (e *editorialAPI) mediaLookup(id string) (api.BlogMedia, bool) {
	media, err := e.svc.MediaItem(id)
	return media, err == nil
}

// preview renders unsaved blocks with the same renderer and sanitizer the
// public page uses, so what the author sees is what readers get.
func (e *editorialAPI) preview(w http.ResponseWriter, r *http.Request, _ api.EditorialMember) {
	var body api.SavePostRequest
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	blocks, err := editorial.NormalizeBlocks(body.Blocks)
	if err != nil {
		e.writeErr(w, err)
		return
	}
	response := map[string]any{"rendered_html": editorial.Render(blocks, e.mediaLookup), "reading_minutes": editorial.ReadingMinutes(blocks)}
	if body.Cover != nil {
		if media, ok := e.mediaLookup(body.Cover.MediaID); ok {
			response["cover"] = editorial.Image(media, body.Cover.Alt)
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (e *editorialAPI) listMedia(w http.ResponseWriter, _ *http.Request, _ api.EditorialMember) {
	items := e.svc.MediaItems()
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (e *editorialAPI) upload(w http.ResponseWriter, r *http.Request, member api.EditorialMember) {
	r.Body = http.MaxBytesReader(w, r.Body, editorial.MaxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		editorialUploads.WithLabelValues("too_large").Inc()
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Images are limited to 10 MB.")
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	file, _, err := r.FormFile("file")
	if err != nil {
		file, _, err = r.FormFile("image")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Attach the image as the multipart field \"file\".")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, editorial.MaxUploadBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "could not read the upload")
		return
	}
	if len(data) > editorial.MaxUploadBytes {
		editorialUploads.WithLabelValues("too_large").Inc()
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Images are limited to 10 MB.")
		return
	}
	media, err := e.svc.Upload(r.Context(), member, data, r.FormValue("alt"), r.FormValue("caption"), r.FormValue("attribution"))
	switch {
	case errors.Is(err, editorial.ErrUnsupportedImage):
		editorialUploads.WithLabelValues("unsupported").Inc()
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", err.Error())
		return
	case errors.Is(err, editorial.ErrImageDimensions), errors.Is(err, editorial.ErrTooLarge):
		editorialUploads.WithLabelValues("too_large").Inc()
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
		return
	case err != nil:
		editorialUploads.WithLabelValues("error").Inc()
		e.writeErr(w, err)
		return
	}
	editorialUploads.WithLabelValues("ok").Inc()
	image := editorial.Image(media, media.Alt)
	// "success"/"file" follow the Editor.js uploader contract.
	writeJSON(w, http.StatusCreated, map[string]any{"success": 1, "file": map[string]any{
		"url": image.URL, "srcset": image.SrcSet, "media_id": media.ID, "width": media.Width, "height": media.Height,
	}, "media": media})
}

func (e *editorialAPI) updateMedia(w http.ResponseWriter, r *http.Request, member api.EditorialMember) {
	var body struct {
		Alt         *string `json:"alt"`
		Caption     *string `json:"caption"`
		Attribution *string `json:"attribution"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	media, err := e.svc.UpdateMedia(member, r.PathValue("id"), body.Alt, body.Caption, body.Attribution)
	if err != nil {
		e.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, media)
}

func (e *editorialAPI) serveMedia(w http.ResponseWriter, r *http.Request) {
	media, err := e.svc.MediaItem(r.PathValue("id"))
	var variant *api.MediaVariant
	if err == nil {
		for i := range media.Variants {
			if media.Variants[i].Name == r.PathValue("variant") {
				variant = &media.Variants[i]
			}
		}
		// Small images have fewer variants; serve the largest one we have.
		if variant == nil && len(media.Variants) > 0 && slices.Contains([]string{"480", "960", "1600"}, r.PathValue("variant")) {
			variant = &media.Variants[len(media.Variants)-1]
		}
	}
	public := err == nil && variant != nil && e.svc.MediaPublic(media)
	if variant == nil || (!public && func() bool { _, err := e.svc.ResolveSession(sessionToken(r)); return err != nil }()) {
		http.NotFound(w, r)
		return
	}
	body, err := e.svc.Media.Get(r.Context(), variant.Key)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", variant.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if public {
		w.Header().Set("Cache-Control", "public, max-age=300")
	} else {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	_, _ = io.Copy(w, body)
}

func (e *editorialAPI) listMembers(w http.ResponseWriter, _ *http.Request, member api.EditorialMember) {
	counts := map[string]int{}
	for _, post := range e.svc.Posts() {
		counts[post.AuthorSubject]++
	}
	items := []map[string]any{}
	for _, m := range e.svc.Members() {
		item := map[string]any{"subject": m.Subject, "display_name": m.DisplayName, "role": m.Role, "disabled": m.Disabled, "posts": counts[m.Subject]}
		if editorial.AtLeast(member.Role, api.EditorialAdmin) {
			item["email"], item["created_at"], item["created_by"] = m.Email, m.CreatedAt, m.CreatedBy
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "can_manage": editorial.AtLeast(member.Role, api.EditorialAdmin)})
}

func (e *editorialAPI) putMember(w http.ResponseWriter, r *http.Request, member api.EditorialMember) {
	var body api.EditorialMember
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	body.Subject = r.PathValue("subject")
	saved, err := e.svc.PutMember(member, body)
	if err != nil {
		e.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (e *editorialAPI) deleteMember(w http.ResponseWriter, r *http.Request, member api.EditorialMember) {
	if err := e.svc.DeleteMember(member, r.PathValue("subject")); err != nil {
		e.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// blogSettings is the editorial_settings/site document.
type blogSettings struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

func (e *editorialAPI) settings() blogSettings {
	value, err := store.GetDoc[blogSettings](e.server.store, editorial.SettingsKind, "site")
	if err != nil || value.Title == "" {
		return blogSettings{Title: "Notes from building the platform.", Description: "Detailed, practical writing about machine learning infrastructure, AI systems, developer workspaces, security, data, and operations."}
	}
	return value
}

func (e *editorialAPI) getSettings(w http.ResponseWriter, _ *http.Request, _ api.EditorialMember) {
	writeJSON(w, http.StatusOK, e.settings())
}

func (e *editorialAPI) putSettings(w http.ResponseWriter, r *http.Request, member api.EditorialMember) {
	if !editorial.AtLeast(member.Role, api.EditorialAdmin) {
		writeError(w, http.StatusForbidden, "forbidden", editorial.ErrForbidden.Error())
		return
	}
	var body blogSettings
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	body.Title = strings.TrimSpace(editorial.PlainText(body.Title))
	body.Description = strings.TrimSpace(editorial.PlainText(body.Description))
	if len(body.Title) < 3 || len(body.Title) > 120 || len(body.Description) > 400 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "The blog title needs 3 to 120 characters and the description at most 400.")
		return
	}
	saved, err := store.UpdateDoc(e.server.store, editorial.SettingsKind, "site", func(blogSettings, bool) (blogSettings, error) { return body, nil }, "editorial.settings.updated", member.Subject)
	if err != nil {
		e.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

// --- public reading API ---

func (e *editorialAPI) public(post api.EditorialPost, full bool) api.PublicBlogPost {
	out := api.PublicBlogPost{
		ID: post.ID, Slug: post.Slug, Title: post.Title, Summary: post.Summary, Author: post.Author, Tags: post.Tags, Status: post.Status,
		CreatedAt: post.CreatedAt, UpdatedAt: post.UpdatedAt, PublishedAt: post.PublishedAt, ReadingMinutes: editorial.ReadingMinutes(post.Blocks), SEO: post.SEO,
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if post.Cover != nil {
		if media, ok := e.mediaLookup(post.Cover.MediaID); ok {
			image := editorial.Image(media, post.Cover.Alt)
			out.Cover = &image
		}
	}
	if full {
		out.Blocks = post.Blocks
		out.RenderedHTML = editorial.Render(post.Blocks, e.mediaLookup)
		out.Content = editorial.ToMarkdown(post.Blocks)
	}
	return out
}

func publishedTime(post api.EditorialPost) time.Time {
	if post.PublishedAt != nil {
		return *post.PublishedAt
	}
	return post.CreatedAt
}

func (e *editorialAPI) published() []api.EditorialPost {
	var posts []api.EditorialPost
	for _, post := range e.svc.Posts() {
		if post.Status == api.PostPublished {
			posts = append(posts, post)
		}
	}
	slices.SortStableFunc(posts, func(a, b api.EditorialPost) int { return publishedTime(b).Compare(publishedTime(a)) })
	return posts
}

func (e *editorialAPI) publicPosts(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	tag, author, search := strings.ToLower(query.Get("tag")), query.Get("author"), strings.ToLower(strings.TrimSpace(query.Get("q")))
	items := []api.PublicBlogPost{}
	for _, post := range e.published() {
		if tag != "" && !slices.ContainsFunc(post.Tags, func(value string) bool { return strings.ToLower(value) == tag }) {
			continue
		}
		if author != "" && post.Author != author {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(post.Title+" "+post.Summary+" "+strings.Join(post.Tags, " ")+" "+editorial.TextOf(post.Blocks)), search) {
			continue
		}
		items = append(items, e.public(post, false))
	}
	w.Header().Set("Cache-Control", "public, no-cache")
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "site": e.settings()})
}

func (e *editorialAPI) publicPost(w http.ResponseWriter, r *http.Request) {
	post, err := e.svc.PostBySlug(r.PathValue("slug"))
	if err != nil {
		post, err = e.svc.Post(r.PathValue("slug"))
	}
	if err != nil || post.Status != api.PostPublished {
		writeError(w, http.StatusNotFound, "not_found", "blog post not found")
		return
	}
	out := e.public(post, true)
	all := e.published()
	bySlug := map[string]api.EditorialPost{}
	for _, candidate := range all {
		bySlug[candidate.Slug] = candidate
	}
	for _, slug := range editorial.RelatedByTags(post, all, 3) {
		out.Related = append(out.Related, e.public(bySlug[slug], false))
	}
	w.Header().Set("Cache-Control", "public, no-cache")
	writeJSON(w, http.StatusOK, out)
}
