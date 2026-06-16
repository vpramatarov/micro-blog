// Package posts implements the /posts/* (public reads) and /admin/posts/*
// (writes + role-aware list) HTTP handlers. Public reads use hashid codes;
// admin operations use raw numeric ids.
package posts

import (
	"encoding/json"
	"errors"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	categoryRepository "github.com/vpramatarov/micro-blog/internal/api/repository/categories"
	"github.com/vpramatarov/micro-blog/internal/api/repository/jobs"
	postRepository "github.com/vpramatarov/micro-blog/internal/api/repository/posts"
	tagRepository "github.com/vpramatarov/micro-blog/internal/api/repository/tags"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/imagex"
	"github.com/vpramatarov/micro-blog/internal/markdown"
	"github.com/vpramatarov/micro-blog/internal/shortcode"
	"github.com/vpramatarov/micro-blog/internal/slug"
	"github.com/vpramatarov/micro-blog/internal/uploads"
	"github.com/vpramatarov/micro-blog/internal/validation"
)

const roleAuthor string = "Author"

// MultipartBodyLimit caps the multipart body for write endpoints. 5 MiB image payload + ~64 KiB envelope for form boundary + the JSON `data` field.
const MultipartBodyLimit = 5*1024*1024 + 64*1024

// searchMinQueryLen is the shortest accepted `/search?q={term}` (in runes). Shorter queries return an empty result set.
const searchMinQueryLen = 3

// errImageRejected is the sentinel returned by Create Post's slug.AllocateForName
// create-closure when the image pipeline has already written its own response
// envelope (e.g. 415 unsupported_media_type, 413 payload_too_large) via
// readAndValidateImage / saveImageAndEnqueue. The handler's error switch
// checks the imageRespWritten flag and skips the default 500 envelope.
var errImageRejected = errors.New("posts: image rejected (response already written)")

// PostResponse is the wire-format view: the scalar post columns plus the hydrated category and tags.
// The embedded postRepository.Post is flattened by encoding/json so the JSON shape is the union of fields, not a nested `post` object.
//
// Tags is intentionally a map[int64]string so clients can index by id without scanning a slice.
// encoding/json stringifies the int keys, producing `{"3":"go","5":"web"}` on the wire.
// The hydration helpers always allocate an empty map so a post with no tags serializes as `{}`, not `null`.
type PostResponse struct {
	postRepository.Post
	Excerpt string           `json:"excerpt"`
	Tags    map[int64]string `json:"tags"`
}

type postWriteRequest struct {
	Title               *string `json:"title"`
	Markdown            *string `json:"markdown_content"`
	CategoryID          *int64  `json:"category_id"`
	TagIDs              []int64 `json:"tag_ids"`
	RemoveFeaturedImage bool    `json:"remove_featured_image"`
	Status              string  `json:"status"`
}

// PostsByCategoryResponse wraps the paginated post list with the parent
// category's identity so the UI can render "Posts in <Category>" without a second API call.
type PostsByCategoryResponse struct {
	Category categoryRepository.Category `json:"category"`
	Items    []PostResponse              `json:"items"`
	Page     int                         `json:"page"`
	PerPage  int                         `json:"per_page"`
	Total    int                         `json:"total"`
}

// PostsByTagResponse — same wrapper for the tag pivot.
type PostsByTagResponse struct {
	Tag     tagRepository.Tag `json:"tag"`
	Items   []PostResponse    `json:"items"`
	Page    int               `json:"page"`
	PerPage int               `json:"per_page"`
	Total   int               `json:"total"`
}

func (r *postWriteRequest) normalize() {
	if r.Title != nil {
		title := strings.TrimSpace(*r.Title)
		r.Title = &title
	}

	if r.Markdown != nil {
		md := strings.TrimSpace(*r.Markdown)
		r.Markdown = &md
	}

	r.Status = strings.TrimSpace(r.Status)
}

func (r *postWriteRequest) ValidateUpdate() validation.Errors {
	e := validation.New()
	if r.Title != nil {
		e.Add("title", validation.Title(*r.Title))
	}

	if r.Markdown != nil {
		e.Add("markdown_content", validation.MarkdownContent(*r.Markdown))
	}

	if r.CategoryID != nil && *r.CategoryID <= 0 {
		e.Add("category_id", "must be a positive id.")
	}

	if r.Status != "" {
		e.Add("status", validation.PostStatus(r.Status))
	}

	return e
}

func (r *postWriteRequest) ValidateCreate() validation.Errors {
	e := r.ValidateUpdate()
	if r.Title == nil {
		e.Add("title", "is required")
	}

	if r.Markdown == nil {
		e.Add("markdown_content", "is required")
	}

	if r.CategoryID == nil {
		e.Add("category_id", "is required")
	}

	return e
}

// Service handles the post endpoints. The Encoder converts numeric ids to hashid `code`s for public URLs;
// nil is tolerated (Code stays empty and `omitempty` keeps it out of the wire format).
type Service struct {
	Posts      *postRepository.Repo
	Categories *categoryRepository.Repo
	Tags       *tagRepository.Repo
	Storage    *uploads.Storage
	Jobs       *jobs.Repo
	Encoder    *shortcode.Encoder
	Log        *slog.Logger
}

func New(
	repo *postRepository.Repo,
	categoriesRepo *categoryRepository.Repo,
	tagsRepo *tagRepository.Repo,
	storage *uploads.Storage,
	jobsRepo *jobs.Repo,
	encoder *shortcode.Encoder,
	log *slog.Logger,
) *Service {
	if log == nil {
		log = slog.Default()
	}

	return &Service{
		Posts:      repo,
		Categories: categoriesRepo,
		Tags:       tagsRepo,
		Storage:    storage,
		Jobs:       jobsRepo,
		Encoder:    encoder,
		Log:        log,
	}
}

// List — GET /posts. Public. Returns every post with a hashid `code` callers can use against GET /posts/{code}. Paginated via ?page / ?per_page.
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	limit, offset, page, perPage, ok := httpx.ParsePagination(w, r)
	if !ok {
		return
	}

	total, err := s.Posts.Count(r.Context(), postRepository.PostStatusPublished)
	if err != nil {
		s.Log.Error("count posts", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts")
	}

	posts, err := s.Posts.List(r.Context(), postRepository.PostStatusPublished, limit, offset)
	if err != nil {
		s.Log.Error("list posts", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts")
		return
	}

	items, err := s.hydrateMany(r, posts)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, httpx.Page[PostResponse]{Items: items, Page: page, PerPage: perPage, Total: total})
}

// GetByCode — GET /p/{code}. Public; decodes the hashid back to a numeric id and serves the post.
// Used by everyone who is not an Admin and by unauthenticated callers.
func (s *Service) GetByCode(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	if s.Encoder == nil {
		s.Log.Error("encoder not configured")
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "post lookup unavailable")
		return
	}

	id, err := s.Encoder.Decode(code)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
		return
	}

	post, err := s.Posts.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, postRepository.ErrPostNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
			return
		}

		s.Log.Error("get post", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load post")
		return
	}

	// Public endpoint: non-published posts must be treated as missing row, otherwise draft URL's can leak existence.
	if post.Status != postRepository.PostStatusPublished {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
		return
	}

	view, err := s.hydrateOne(r, post)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load post")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, view)
}

// GetBySlug — GET /posts/{slug}. Public.
func (s *Service) GetBySlug(w http.ResponseWriter, r *http.Request) {
	slugParam := chi.URLParam(r, "slug")
	if slugParam == "" {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
		return
	}

	post, err := s.Posts.GetBySlug(r.Context(), slugParam)
	if err != nil {
		if errors.Is(err, postRepository.ErrPostNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
			return
		}

		s.Log.Error("get post by slug", "err", err, "slug", slugParam)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load post")
		return
	}

	// Public endpoint: non-published posts must be treated as missing row, otherwise draft URL's can leak existence.
	if post.Status != postRepository.PostStatusPublished {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
		return
	}

	view, err := s.hydrateOne(r, post)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load post")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, view)
}

// ListAdmin — GET /admin/posts. Authenticated; Authors only see their own posts, every other role sees all posts.
// Paginated via ?page / ?per_page. Accepts an optional ?status=draft|published|archived filter; empty means all statuses.
// The numeric `id` is exposed because admins/editors/authors need it to call PUT/DELETE /admin/posts/{id}.
func (s *Service) ListAdmin(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing auth")
		return
	}

	limit, offset, page, perPage, ok := httpx.ParsePagination(w, r)
	if !ok {
		return
	}

	status, ok := parseStatusQuery(w, r)
	if !ok {
		return
	}

	total, err := s.countPostsForRole(r, claims, status)
	if err != nil {
		s.Log.Error("count posts", "err", err, "user_id", claims.UserID, "role", claims.Role)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts (count)")
		return
	}

	posts, err := s.listPostsForRole(r, claims, status, limit, offset)
	if err != nil {
		s.Log.Error("list posts", "err", err, "user_id", claims.UserID, "role", claims.Role)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts (list)")
		return
	}

	items, err := s.hydrateMany(r, posts)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts (hydrate)")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, httpx.Page[PostResponse]{Items: items, Page: page, PerPage: perPage, Total: total})
}

// GetById — GET /admin/post/{id}. Admin role only (enforced by the router's requireAdminMW). Reads a post by its raw numeric id.
func (s *Service) GetById(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseIDParam(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "invalid post id")
		return
	}

	post, err := s.Posts.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, postRepository.ErrPostNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
			return
		}

		s.Log.Error("get post admin", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load post")
		return
	}

	view, err := s.hydrateOne(r, post)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load post")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, view)
}

// Create — POST /admin/posts. Bouncer gates on post:create.
// author_id is taken from the caller's claims so Authors can never spoof another user.
func (s *Service) Create(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing auth")
		return
	}

	req, fileHeader, _, ok := s.parsePostMultipart(w, r, true)
	if !ok {
		return
	}

	req.normalize()
	if req.Status == "" {
		req.Status = postRepository.PostStatusDraft // defaults to "draft" when the client omits the status.
	}

	errs := req.ValidateCreate()
	if !errs.IsEmpty() {
		httpx.WriteValidationError(w, errs)
		return
	}

	// Existence checks happen only after the format pass succeeds — same pattern as users.Create's role_id check.
	if ok := s.validateTaxonomies(w, r, req, errs); !ok {
		return
	}

	html, err := markdown.Render(*req.Markdown)
	if err != nil {
		s.Log.Error("render markdown", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not render markdown")
		return
	}

	base := slug.Generate(*req.Title)
	if base == "" {
		httpx.WriteValidationError(w, map[string]string{"title": "must contain at least one Latin or Cyrillic letter or digit"})
		return
	}

	// Slug allocation + image save + INSERT all live inside slug.AllocateForName.
	// The helper runs Generate on the title FIRST; the title-yields-empty-slug
	// check returns ErrEmptyGeneratedSlug before the closure is ever entered,
	// so we never save an image for a doomed post. Image save itself lives
	// inside the closure, guarded by imageSaveAttempted so it runs exactly
	// once even when a slug-UNIQUE race triggers the closure twice.
	var (
		imagePath          string
		imageSaveAttempted bool
		imageRespWritten   bool // readAndValidateImage / saveImageAndEnqueue wrote their own envelope
	)
	id, err := slug.Allocate(r.Context(), s.Posts, *req.Title, "", 0, s.createFn(w, r, claims, req, fileHeader, html, &imagePath, &imageSaveAttempted, &imageRespWritten))
	if err != nil {
		// Roll the image back so we don't leak orphans on a failed INSERT.
		if imagePath != "" {
			err := s.Storage.DeleteAll(imagePath)
			if err != nil {
				// just log for now.
				s.Log.Warn("rollback image", "err", err, "image_path", imagePath)
			}
		}

		switch {
		case imageRespWritten:
			// readAndValidateImage / saveImageAndEnqueue already wrote their own envelope.
		case errors.Is(err, slug.ErrEmptyGeneratedSlug):
			httpx.WriteValidationError(w, map[string]string{"title": "must contain at least one Latin or Cyrillic letter or digit"})
		default:
			s.Log.Error("create post", "err", err, "user_id", claims.UserID)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not create post")
		}
		return
	}

	if len(req.TagIDs) > 0 {
		if err := s.Tags.ReplaceForPost(r.Context(), id, req.TagIDs); err != nil {
			s.Log.Error("attach tags", "err", err, "post_id", id)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not attach tags")
			return
		}
	}

	view, err := s.loadView(r, id)
	if err != nil {
		s.Log.Error("load created post", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load post")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, view)
}

// Update — PUT /admin/posts/{id}. Bouncer enforces ownership for Authors;
// Admin/Editor pass through to any post. Returns the updated post.
// Image semantics:
//   - file part present                  → REPLACE the existing image
//   - data.remove_featured_image == true → DELETE the existing image
//   - neither                            → KEEP the existing image
func (s *Service) Update(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseIDParam(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "invalid post id")
		return
	}

	req, fileHeader, dataPresent, ok := s.parsePostMultipart(w, r, false)
	if !ok {
		return
	}

	req.normalize()
	errs := req.ValidateUpdate()
	if !errs.IsEmpty() {
		httpx.WriteValidationError(w, errs)
		return
	}

	if ok := s.validateTaxonomies(w, r, req, errs); !ok {
		return
	}

	// Load the existing row up front — we need its current featured_image_path
	// to decide whether to delete on disk after the UPDATE succeeds (replace + clear branches).
	existing, err := s.Posts.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, postRepository.ErrPostNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
			return
		}

		s.Log.Error("get post for update", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update post")
		return
	}
	oldPath := existing.FeaturedImagePath

	title := existing.Title
	if req.Title != nil {
		title = *req.Title
	}

	categoryID := existing.CategoryID
	if req.CategoryID != nil {
		categoryID = *req.CategoryID
	}

	// Status: empty in request means keep existing.
	newStatus := req.Status
	if newStatus == "" {
		newStatus = existing.Status
	}

	mdContent, html := existing.MarkdownContent, existing.HTMLContent
	if req.Markdown != nil {
		mdContent = *req.Markdown
		html, err = markdown.Render(mdContent)
		if err != nil {
			s.Log.Error("render markdown", "err", err)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not render markdown")
			return
		}
	}

	// Resolve the new image path before touching the DB. Possible outcomes:
	//   replace: newPath = freshly-saved path, deleteOld = oldPath
	//   clear  : newPath = "",                deleteOld = oldPath
	//   keep   : newPath = oldPath,           deleteOld = ""
	var newPath, deleteOld string
	switch {
	case fileHeader != nil:
		encoded, format, ext, imgOK := s.readAndValidateImage(w, fileHeader)
		if !imgOK {
			return
		}

		path, imgOK := s.saveImageAndEnqueue(w, r, fileHeader, encoded, format, ext)
		if !imgOK {
			return
		}

		newPath = path
		deleteOld = oldPath
	case req.RemoveFeaturedImage:
		newPath = ""
		deleteOld = oldPath
	default:
		newPath = oldPath
	}

	// Posts always regenerate slug from the new title — no clientSlug input.
	// excludeID=id so the row's own slug doesn't count as a self-collision.
	// AllocateForName runs Generate(title) internally; an empty result surfaces as ErrEmptyGeneratedSlug below (image rollback path covers it).
	_, err = slug.Allocate(r.Context(), s.Posts, title, "", id, s.updateFn(r, id, postRepository.PostUpdate{
		CategoryID: categoryID, Title: title, Markdown: mdContent, HTML: html, FeaturedImagePath: newPath, Status: newStatus,
	}))
	if err != nil {
		// Roll back any freshly-saved image on UPDATE failure so we don't leak orphans.
		// We deliberately do NOT touch oldPath here — the existing post still references it.
		if newPath != "" && newPath != oldPath {
			_ = s.Storage.DeleteAll(newPath)
		}

		switch {
		case errors.Is(err, slug.ErrEmptyGeneratedSlug):
			httpx.WriteValidationError(w, map[string]string{"title": "must contain at least one Latin or Cyrillic letter or digit"})
		case errors.Is(err, postRepository.ErrPostNotFound):
			httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
		default:
			s.Log.Error("update post", "err", err, "id", id)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update post")
		}

		return
	}

	if dataPresent {
		// Tag set is rewritten unconditionally on update — pass nil to clear.
		if err := s.Tags.ReplaceForPost(r.Context(), id, req.TagIDs); err != nil {
			s.Log.Error("replace tags", "err", err, "post_id", id)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update tags")
			return
		}
	}

	if deleteOld != "" {
		if err := s.Storage.DeleteAll(deleteOld); err != nil {
			// Don't fail the request — the row already changed. Log so it can be reaped manually if it ever matters.
			s.Log.Warn("delete old featured image", "err", err, "path", deleteOld)
		}
	}

	view, err := s.loadView(r, id)
	if err != nil {
		s.Log.Error("load updated post", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load post")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, view)
}

// Delete — DELETE /admin/posts/{id}. Bouncer enforces ownership for Authors;
// Admin/Editor can delete any post.
func (s *Service) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseIDParam(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "invalid post id")
		return
	}

	existing, err := s.Posts.GetByID(r.Context(), id)
	if err != nil && !errors.Is(err, postRepository.ErrPostNotFound) {
		s.Log.Error("get post for delete", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not delete post")
		return
	}

	var imagePath string
	if existing != nil {
		imagePath = existing.FeaturedImagePath
	}

	if err := s.Posts.Delete(r.Context(), id); err != nil {
		if errors.Is(err, postRepository.ErrPostNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
			return
		}

		s.Log.Error("delete post", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not delete post")
		return
	}

	if imagePath != "" {
		if err := s.Storage.DeleteAll(imagePath); err != nil {
			s.Log.Warn("delete featured image files", "err", err, "path", imagePath)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListByCategorySlug — GET /categories/{slug}. Public; only published posts in the category are returned. Unknown slug → 404.
// Pagination is the same shape as GET /posts.
func (s *Service) ListByCategorySlug(w http.ResponseWriter, r *http.Request) {
	cat, ok := s.lookupCategoryBySlug(w, r)
	if !ok {
		return
	}

	s.listByCategory(w, r, cat, 0, postRepository.PostStatusPublished)
}

// ListByCategorySlugAdmin — GET /admin/categories/{slug}. Authenticated;
// Author role sees only own posts in this category (closed-loop via claims.UserID), every other role sees all.
// Accepts ?status= (same enum as /admin/posts).
func (s *Service) ListByCategorySlugAdmin(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing auth")
		return
	}

	status, ok := parseStatusQuery(w, r)
	if !ok {
		return
	}

	cat, ok := s.lookupCategoryBySlug(w, r)
	if !ok {
		return
	}

	var authorID int64
	if claims.Role == roleAuthor {
		authorID = claims.UserID
	}

	s.listByCategory(w, r, cat, authorID, status)
}

// ListByTagSlug — GET /tags/{slug}. Public counterpart of the category endpoint; only published posts in the tag are returned.
func (s *Service) ListByTagSlug(w http.ResponseWriter, r *http.Request) {
	tag, ok := s.lookupTagBySlug(w, r)
	if !ok {
		return
	}

	s.listByTag(w, r, tag, 0, postRepository.PostStatusPublished)
}

// ListByTagSlugAdmin — GET /admin/tags/{slug}. Same role-aware filter as the category admin endpoint.
func (s *Service) ListByTagSlugAdmin(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing auth")
		return
	}

	status, ok := parseStatusQuery(w, r)
	if !ok {
		return
	}

	tag, ok := s.lookupTagBySlug(w, r)
	if !ok {
		return
	}

	var authorID int64
	if claims.Role == roleAuthor {
		authorID = claims.UserID
	}

	s.listByTag(w, r, tag, authorID, status)
}

func (s *Service) Search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	ctx := r.Context()
	limit, offset, page, perPage, ok := httpx.ParsePagination(w, r)
	if !ok {
		return
	}

	if utf8.RuneCountInString(q) < searchMinQueryLen {
		httpx.WriteJSON(w, http.StatusOK, httpx.Page[PostResponse]{Items: []PostResponse{}, Page: page, PerPage: perPage, Total: 0})
		return
	}

	total, err := s.Posts.CountSearch(ctx, q, postRepository.PostStatusPublished)
	if err != nil {
		s.Log.Error("count posts", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts")
		return
	}

	posts, err := s.Posts.Search(ctx, q, postRepository.PostStatusPublished, limit, offset)
	if err != nil {
		s.Log.Error("list posts", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts")
		return
	}

	items, err := s.hydrateMany(r, posts)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, httpx.Page[PostResponse]{Items: items, Page: page, PerPage: perPage, Total: total})
}

// lookupCategoryBySlug resolves the URL slug to a row or writes the standard 404 envelope.
// Returns (nil, false) on miss or on a 5xx already-written.
func (s *Service) lookupCategoryBySlug(w http.ResponseWriter, r *http.Request) (*categoryRepository.Category, bool) {
	slugParam := chi.URLParam(r, "slug")
	if slugParam == "" {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "category not found")
		return nil, false
	}

	cat, err := s.Categories.GetBySlug(r.Context(), slugParam)
	if err != nil {
		if errors.Is(err, categoryRepository.ErrCategoryNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "category not found")
			return nil, false
		}

		s.Log.Error("get category by slug", "err", err, "slug", slugParam)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load category")
		return nil, false
	}

	return cat, true
}

func (s *Service) lookupTagBySlug(w http.ResponseWriter, r *http.Request) (*tagRepository.Tag, bool) {
	slugParam := chi.URLParam(r, "slug")
	if slugParam == "" {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "tag not found")
		return nil, false
	}

	tag, err := s.Tags.GetBySlug(r.Context(), slugParam)
	if err != nil {
		if errors.Is(err, tagRepository.ErrTagNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "tag not found")
			return nil, false
		}
		s.Log.Error("get tag by slug", "err", err, "slug", slugParam)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load tag")
		return nil, false
	}

	return tag, true
}

// listByCategory is the shared body for the public and admin endpoints.
// authorID=0 / status="" mean "no filter" — sentinels follow the repo layer convention.
func (s *Service) listByCategory(w http.ResponseWriter, r *http.Request, cat *categoryRepository.Category, authorID int64, status string) {
	limit, offset, page, perPage, ok := httpx.ParsePagination(w, r)
	if !ok {
		return
	}

	total, err := s.Posts.CountByCategoryID(r.Context(), cat.ID, authorID, status)
	if err != nil {
		s.Log.Error("count posts by category", "err", err, "category_id", cat.ID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts")
		return
	}

	rows, err := s.Posts.ListByCategoryID(r.Context(), cat.ID, authorID, status, limit, offset)
	if err != nil {
		s.Log.Error("list posts by category", "err", err, "category_id", cat.ID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts")
		return
	}

	items, err := s.hydrateMany(r, rows)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, PostsByCategoryResponse{
		Category: *cat, Items: items, Page: page, PerPage: perPage, Total: total,
	})
}

func (s *Service) listByTag(w http.ResponseWriter, r *http.Request, tag *tagRepository.Tag, authorID int64, status string) {
	limit, offset, page, perPage, ok := httpx.ParsePagination(w, r)
	if !ok {
		return
	}

	total, err := s.Posts.CountByTagID(r.Context(), tag.ID, authorID, status)
	if err != nil {
		s.Log.Error("count posts by tag", "err", err, "tag_id", tag.ID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts")
		return
	}

	rows, err := s.Posts.ListByTagID(r.Context(), tag.ID, authorID, status, limit, offset)
	if err != nil {
		s.Log.Error("list posts by tag", "err", err, "tag_id", tag.ID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts")
		return
	}

	items, err := s.hydrateMany(r, rows)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, PostsByTagResponse{
		Tag: *tag, Items: items, Page: page, PerPage: perPage, Total: total,
	})
}

func (s *Service) listPostsForRole(r *http.Request, claims *auth.Claims, status string, limit, offset int) ([]postRepository.Post, error) {
	if claims.Role == roleAuthor {
		return s.Posts.ListByAuthor(r.Context(), claims.UserID, status, limit, offset)
	}

	return s.Posts.List(r.Context(), status, limit, offset)
}

func (s *Service) countPostsForRole(r *http.Request, claims *auth.Claims, status string) (int, error) {
	if claims.Role == roleAuthor {
		return s.Posts.CountByAuthor(r.Context(), claims.UserID, status)
	}

	return s.Posts.Count(r.Context(), status)
}

// loadView re-fetches the post and hydrates it for a single-row response.
func (s *Service) loadView(r *http.Request, id int64) (*PostResponse, error) {
	post, err := s.Posts.GetByID(r.Context(), id)
	if err != nil {
		return nil, err
	}

	return s.hydrateOne(r, post)
}

// hydrateOne attaches Tags & code to a single post.
func (s *Service) hydrateOne(r *http.Request, post *postRepository.Post) (*PostResponse, error) {
	if s.Encoder != nil {
		if code, err := s.Encoder.Encode(post.ID); err == nil {
			post.Code = code
		}
	}

	view := &PostResponse{Post: *post, Excerpt: markdown.ToText(post.MarkdownContent), Tags: make(map[int64]string)}
	tagSlice, err := s.Tags.ListForPost(r.Context(), post.ID)
	if err != nil {
		s.Log.Error("hydrate tags", "err", err, "post_id", post.ID)
		return nil, err
	}

	for _, t := range tagSlice {
		view.Tags[t.ID] = t.Name
	}

	return view, nil
}

// hydrateMany batches tags hydration across a page of posts.
func (s *Service) hydrateMany(r *http.Request, posts []postRepository.Post) ([]PostResponse, error) {
	items := make([]PostResponse, len(posts))
	postIDs := make([]int64, len(posts))
	for i, p := range posts {
		postIDs[i] = p.ID

		if s.Encoder != nil {
			if code, err := s.Encoder.Encode(posts[i].ID); err == nil {
				posts[i].Code = code
			}
		}
	}

	tagsByPost, err := s.Tags.ListForPosts(r.Context(), postIDs)
	if err != nil {
		s.Log.Error("hydrate tags", "err", err)
		return nil, err
	}

	for i, p := range posts {
		view := PostResponse{Post: p, Excerpt: markdown.ToText(p.MarkdownContent), Tags: make(map[int64]string)}
		for _, t := range tagsByPost[p.ID] {
			view.Tags[t.ID] = t.Name
		}

		items[i] = view
	}

	return items, nil
}

// validateTaxonomies performs the second-pass DB existence checks for category_id (must point at an existing row) and tag_ids (every id must exist).
// Errors accumulate into `errs` and are written as a 400 validation envelope.
// Returns false when the caller should stop (either a 400 or a 500 has already been written).
func (s *Service) validateTaxonomies(w http.ResponseWriter, r *http.Request, req postWriteRequest, errs validation.Errors) bool {
	if req.CategoryID != nil {
		exists, err := s.Categories.Exists(r.Context(), *req.CategoryID)
		if err != nil {
			s.Log.Error("category exists check", "err", err, "category_id", req.CategoryID)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not validate category")
			return false
		}

		if !exists {
			errs.Add("category_id", "does not exist")
		}
	}

	if len(req.TagIDs) > 0 {
		missing, err := s.Tags.MissingIDs(r.Context(), req.TagIDs)
		if err != nil {
			s.Log.Error("tags exists check", "err", err)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not validate tags")
			return false
		}

		if len(missing) > 0 {
			errs.Add("tag_ids", "one or more tag ids do not exist")
		}
	}

	if !errs.IsEmpty() {
		httpx.WriteValidationError(w, errs)
		return false
	}

	return true
}

// parsePostMultipart pulls the JSON fields out of the "data" form part and the optional "featured_image" file part.
// Caps the body at MultipartBodyLimit via http.MaxBytesReader so a single endpoint can accept
// a larger payload than the global LimitBody middleware while still being bounded.
//
// dataRequired controls whether the "data" form field is mandantory.
//
// Returns (request, fileHeader, dataPresent, ok)
func (s *Service) parsePostMultipart(w http.ResponseWriter, r *http.Request, dataRequired bool) (postWriteRequest, *multipart.FileHeader, bool, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, MultipartBodyLimit)
	if err := r.ParseMultipartForm(MultipartBodyLimit); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds 5 MB limit")
			return postWriteRequest{}, nil, false, false
		}

		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "request body is not valid multipart/form-data")
		return postWriteRequest{}, nil, false, false
	}

	// Optional file part.
	var header *multipart.FileHeader
	if r.MultipartForm != nil {
		if files, ok := r.MultipartForm.File["featured_image"]; ok && len(files) > 0 {
			header = files[0]
		}
	}

	rawData := r.FormValue("data")
	if rawData == "" {
		switch {
		case dataRequired:
			httpx.WriteValidationError(w, map[string]string{"data": "is required (JSON-encoded post fields)"})
			return postWriteRequest{}, nil, false, false
		case header == nil:
			httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "provide a \"data\" field, a \"featured_image\" file or both.")
			return postWriteRequest{}, nil, false, false
		default:
			return postWriteRequest{}, header, false, true
		}
	}

	var req postWriteRequest
	if err := json.Unmarshal([]byte(rawData), &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "\"data\" form field is not valid JSON")
		return postWriteRequest{}, nil, false, false
	}

	return req, header, true, true
}

// readAndValidateImage reads the multipart file into memory, decodes it for validation, then re-encodes web-optimized.
// Returns the encoded bytes plus the canonical format ("jpeg"|"png") and extension (".jpg"|".png").
// On any error writes the response envelope and returns false.
func (s *Service) readAndValidateImage(w http.ResponseWriter, header *multipart.FileHeader) (encoded []byte, format, ext string, ok bool) {
	src, err := header.Open()
	if err != nil {
		s.Log.Error("open featured image", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not read uploaded file")
		return nil, "", "", false
	}
	defer src.Close()

	img, format, ext, err := imagex.ValidateAndDecode(src)
	if err != nil {
		switch {
		case errors.Is(err, imagex.ErrUnsupportedFormat):
			httpx.WriteError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "only jpeg and png images are accepted")
		case errors.Is(err, imagex.ErrTooSmall):
			httpx.WriteValidationError(w, map[string]string{"featured_image": "image must be at least 800x800 pixels"})
		default:
			httpx.WriteValidationError(w, map[string]string{"featured_image": "could not decode image"})
		}

		return nil, "", "", false
	}

	encoded, err = imagex.EncodeBytes(img, format)
	if err != nil {
		s.Log.Error("encode featured image", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not process image")
		return nil, "", "", false
	}

	return encoded, format, ext, true
}

// saveImageAndEnqueue writes the encoded image to disk and queues the variant-generation job. Returns the relative path stored in the DB.
func (s *Service) saveImageAndEnqueue(w http.ResponseWriter, r *http.Request, header *multipart.FileHeader, encoded []byte, format, ext string) (string, bool) {
	relPath, err := s.Storage.SaveOriginal(time.Now().UTC(), header.Filename, ext, encoded)
	if err != nil {
		s.Log.Error("save original", "err", err, "filename", header.Filename)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not save image")
		return "", false
	}

	payload, _ := json.Marshal(imagex.VariantsPayload{Path: relPath, Format: format})
	if _, err := s.Jobs.Enqueue(r.Context(), "image_variants", payload); err != nil {
		// Enqueue failed but the original is already on disk. Roll back the file so the post doesn't reference an image without variants.
		_ = s.Storage.DeleteAll(relPath)
		s.Log.Error("enqueue variants job", "err", err, "path", relPath)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not schedule image processing")
		return "", false
	}

	return relPath, true
}

func (s *Service) createFn(
	w http.ResponseWriter,
	r *http.Request,
	claims *auth.Claims,
	req postWriteRequest,
	fileHeader *multipart.FileHeader,
	html string,
	imagePath *string,
	imageSaveAttempted *bool,
	imageRespWritten *bool,
) func(string) (int64, error) {
	return func(slugCandidate string) (int64, error) {
		if !*imageSaveAttempted && fileHeader != nil {
			*imageSaveAttempted = true // set BEFORE the save so a panic mid-save still blocks a retry attempt
			encoded, format, ext, ok := s.readAndValidateImage(w, fileHeader)
			if !ok {
				*imageRespWritten = true
				return 0, errImageRejected
			}

			path, ok := s.saveImageAndEnqueue(w, r, fileHeader, encoded, format, ext)
			if !ok {
				*imageRespWritten = true
				return 0, errImageRejected
			}

			*imagePath = path
		}

		return s.Posts.Create(r.Context(), postRepository.PostInsert{
			AuthorID: claims.UserID, CategoryID: *req.CategoryID, Title: *req.Title,
			Markdown: *req.Markdown, HTML: html, Slug: slugCandidate,
			FeaturedImagePath: *imagePath, Status: req.Status,
		})
	}
}

func (s *Service) updateFn(r *http.Request, id int64, model postRepository.PostUpdate) func(string) (struct{}, error) {
	return func(slugCandidate string) (struct{}, error) {
		return struct{}{}, s.Posts.Update(r.Context(), id, model)
	}
}

func parseStatusQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		return "", true
	}

	if msg := validation.PostStatus(status); msg != "" {
		httpx.WriteValidationError(w, map[string]string{"status": msg})
		return "", false
	}

	return status, true
}
