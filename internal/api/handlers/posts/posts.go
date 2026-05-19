// Package posts implements the /posts/* (public reads) and /admin/posts/*
// (writes + role-aware list) HTTP handlers. Public reads use hashid codes;
// admin operations use raw numeric ids.
package posts

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	categoryRepository "github.com/vpramatarov/micro-blog/internal/api/repository/categories"
	postRepository "github.com/vpramatarov/micro-blog/internal/api/repository/posts"
	tagRepository "github.com/vpramatarov/micro-blog/internal/api/repository/tags"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/markdown"
	"github.com/vpramatarov/micro-blog/internal/shortcode"
	"github.com/vpramatarov/micro-blog/internal/slug"
	"github.com/vpramatarov/micro-blog/internal/validation"
)

const roleAuthor string = "Author"

// PostResponse is the wire-format view: the scalar post columns plus the hydrated category and tags.
// The embedded postRepository.Post is flattened by encoding/json so the JSON shape is the union of fields, not a nested `post` object.
//
// Tags is intentionally a map[int64]string so clients can index by id without scanning a slice.
// encoding/json stringifies the int keys, producing `{"3":"go","5":"web"}` on the wire.
// The hydration helpers always allocate an empty map so a post with no tags serializes as `{}`, not `null`.
type PostResponse struct {
	postRepository.Post
	Tags map[int64]string `json:"tags"`
}

type postWriteRequest struct {
	Title      string `json:"title"`
	Markdown   string `json:"markdown_content"`
	CategoryID int64  `json:"category_id"`
}

func (r *postWriteRequest) normalize() {
	r.Title = strings.TrimSpace(r.Title)
	r.Markdown = strings.TrimSpace(r.Markdown)
}

func (r *postWriteRequest) Validate() validation.Errors {
	e := validation.New()
	e.Add("title", validation.Title(r.Title))
	e.Add("markdown_content", validation.MarkdownContent(r.Markdown))
	if r.CategoryID <= 0 {
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
	Encoder    *shortcode.Encoder
	Log        *slog.Logger
}

func New(repo *postRepository.Repo, categoriesRepo *categoryRepository.Repo, tagsRepo *tagRepository.Repo, encoder *shortcode.Encoder, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}

	return &Service{Posts: repo, Categories: categoriesRepo, Tags: tagsRepo, Encoder: encoder, Log: log}
}

// List — GET /posts. Public. Returns every post with a hashid `code` callers can use against GET /posts/{code}. Paginated via ?page / ?per_page.
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	limit, offset, page, perPage, ok := httpx.ParsePagination(w, r)
	if !ok {
		return
	}

	total, err := s.Posts.Count(r.Context())
	if err != nil {
		s.Log.Error("count posts", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts")
	}

	posts, err := s.Posts.List(r.Context(), limit, offset)
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

	view, err := s.hydrateOne(r, post)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load post")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, view)
}

// ListAdmin — GET /admin/posts. Authenticated; Authors only see their own posts, every other role sees all posts.
// Paginated via ?page / ?per_page.
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

	total, err := s.countPostsForRole(r, claims)
	if err != nil {
		s.Log.Error("count posts", "err", err, "user_id", claims.UserID, "role", claims.Role)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list posts")
		return
	}

	posts, err := s.listPostsForRole(r, claims, limit, offset)
	if err != nil {
		s.Log.Error("list posts", "err", err, "user_id", claims.UserID, "role", claims.Role)
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

	var req postWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "request body is not valid JSON")
		return
	}

	req.normalize()
	errs := req.Validate()
	if !errs.IsEmpty() {
		httpx.WriteValidationError(w, errs)
		return
	}

	// Existence checks happen only after the format pass succeeds — same pattern as users.Create's role_id check.
	if ok := s.validateTaxonomies(w, r, req, errs); !ok {
		return
	}

	html, err := markdown.Render(req.Markdown)
	if err != nil {
		s.Log.Error("render markdown", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not render markdown")
		return
	}

	base := slug.Generate(req.Title)
	if base == "" {
		httpx.WriteValidationError(w, map[string]string{"title": "must contain at least one Latin or Cyrillic letter or digit"})
		return
	}

	id, err := s.createWithSlug(r, postRepository.PostInsert{
		AuthorID: claims.UserID, CategoryID: req.CategoryID,
		Title: req.Title, Markdown: req.Markdown, HTML: html, Slug: base,
	})
	if err != nil {
		s.Log.Error("create post", "err", err, "user_id", claims.UserID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not create post")
		return
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
func (s *Service) Update(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseIDParam(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "invalid post id")
		return
	}

	var req postWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "request body is not valid JSON")
		return
	}

	req.normalize()
	errs := req.Validate()
	if !errs.IsEmpty() {
		httpx.WriteValidationError(w, errs)
		return
	}

	if ok := s.validateTaxonomies(w, r, req, errs); !ok {
		return
	}

	html, err := markdown.Render(req.Markdown)
	if err != nil {
		s.Log.Error("render markdown", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not render markdown")
		return
	}

	base := slug.Generate(req.Title)
	if base == "" {
		httpx.WriteValidationError(w, map[string]string{"title": "must contain at least one Latin or Cyrillic letter or digit"})
		return
	}

	if err := s.updateWithSlug(r, id, postRepository.PostUpdate{
		CategoryID: req.CategoryID, Title: req.Title, Markdown: req.Markdown, HTML: html, Slug: base,
	}); err != nil {
		if errors.Is(err, postRepository.ErrPostNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
			return
		}

		s.Log.Error("update post", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update post")
		return
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

	if err := s.Posts.Delete(r.Context(), id); err != nil {
		if errors.Is(err, postRepository.ErrPostNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
			return
		}

		s.Log.Error("delete post", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not delete post")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) listPostsForRole(r *http.Request, claims *auth.Claims, limit, offset int) ([]postRepository.Post, error) {
	if claims.Role == roleAuthor {
		return s.Posts.ListByAuthor(r.Context(), claims.UserID, limit, offset)
	}

	return s.Posts.List(r.Context(), limit, offset)
}

func (s *Service) countPostsForRole(r *http.Request, claims *auth.Claims) (int, error) {
	if claims.Role == roleAuthor {
		return s.Posts.CountByAuthor(r.Context(), claims.UserID)
	}

	return s.Posts.Count(r.Context())
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

	view := &PostResponse{Post: *post, Tags: make(map[int64]string)}
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
			if code, err := s.Encoder.Encode(posts[i].ID); err != nil {
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
		view := PostResponse{Post: p, Tags: make(map[int64]string)}
		for _, t := range tagsByPost[p.ID] {
			view.Tags[t.ID] = t.Name
		}

		items[i] = view
	}

	return items, nil
}

// createWithSlug resolves a free slug variant and inserts the post.
// Retries once on the rare race where two concurrent writers both pick the same suffix between FindAvailableSlug and INSERT.
func (s *Service) createWithSlug(r *http.Request, ins postRepository.PostInsert) (int64, error) {
	for attempt := 0; attempt < 2; attempt++ {
		candidate, err := s.Posts.FindAvailableSlug(r.Context(), ins.Slug, 0)
		if err != nil {
			return 0, err
		}

		insertion := ins
		insertion.Slug = candidate
		id, err := s.Posts.Create(r.Context(), insertion)
		if err == nil {
			return id, nil
		}

		if !errors.Is(err, postRepository.ErrPostDuplicateSlug) {
			return 0, err
		}
		// Lost the race against another writer — try again.
	}

	return 0, errors.New("posts: could not allocate a free slug after retry")
}

func (s *Service) updateWithSlug(r *http.Request, id int64, postUpdate postRepository.PostUpdate) error {
	for attempt := 0; attempt < 2; attempt++ {
		candidate, err := s.Posts.FindAvailableSlug(r.Context(), postUpdate.Slug, id)
		if err != nil {
			return err
		}

		write := postUpdate
		write.Slug = candidate
		err = s.Posts.Update(r.Context(), id, write)
		if err == nil {
			return nil
		}

		if !errors.Is(err, postRepository.ErrPostDuplicateSlug) {
			return err
		}
	}

	return errors.New("posts: could not allocate a free slug after retry")
}

// validateTaxonomies performs the second-pass DB existence checks for category_id (must point at an existing row) and tag_ids (every id must exist).
// Errors accumulate into `errs` and are written as a 400 validation envelope.
// Returns false when the caller should stop (either a 400 or a 500 has already been written).
func (s *Service) validateTaxonomies(w http.ResponseWriter, r *http.Request, req postWriteRequest, errs validation.Errors) bool {
	exists, err := s.Categories.Exists(r.Context(), req.CategoryID)
	if err != nil {
		s.Log.Error("category exists check", "err", err, "category_id", req.CategoryID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not validate category")
		return false
	}

	if !exists {
		errs.Add("category_id", "does not exist")
	}

	if !errs.IsEmpty() {
		httpx.WriteValidationError(w, errs)
		return false
	}

	return true
}
