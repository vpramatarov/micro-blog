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
	postRepo "github.com/vpramatarov/micro-blog/internal/api/repository/posts"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/markdown"
	"github.com/vpramatarov/micro-blog/internal/shortcode"
	"github.com/vpramatarov/micro-blog/internal/validation"
)

const roleAuthor string = "Author"

type postWriteRequest struct {
	Title    string `json:"title"`
	Markdown string `json:"markdown_content"`
}

func (r *postWriteRequest) normalize() {
	r.Title = strings.TrimSpace(r.Title)
	r.Markdown = strings.TrimSpace(r.Markdown)
}

func (r *postWriteRequest) Validate() validation.Errors {
	e := validation.New()
	e.Add("title", validation.Title(r.Title))
	e.Add("markdown_content", validation.MarkdownContent(r.Markdown))
	return e
}

// Service handles the post endpoints. The Encoder converts numeric ids to hashid `code`s for public URLs;
// nil is tolerated (Code stays empty and `omitempty` keeps it out of the wire format).
type Service struct {
	Posts   *postRepo.Repo
	Encoder *shortcode.Encoder
	Log     *slog.Logger
}

func New(r *postRepo.Repo, encoder *shortcode.Encoder, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}

	return &Service{Posts: r, Encoder: encoder, Log: log}
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

	s.attachCodes(posts)
	httpx.WriteJSON(w, http.StatusOK, httpx.Page[postRepo.Post]{Items: posts, Page: page, PerPage: perPage, Total: total})
}

// GetByCode — GET /posts/{code}. Public; decodes the hashid back to a numeric id and serves the post.
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
		if errors.Is(err, postRepo.ErrPostNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
			return
		}

		s.Log.Error("get post", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load post")
		return
	}

	post.Code = code
	httpx.WriteJSON(w, http.StatusOK, post)
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

	s.attachCodes(posts)
	httpx.WriteJSON(w, http.StatusOK, httpx.Page[postRepo.Post]{Items: posts, Page: page, PerPage: perPage, Total: total})
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
		if errors.Is(err, postRepo.ErrPostNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
			return
		}

		s.Log.Error("get post admin", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load post")
		return
	}

	s.attachCode(post)
	httpx.WriteJSON(w, http.StatusOK, post)
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
	if errs := req.Validate(); !errs.IsEmpty() {
		httpx.WriteValidationError(w, errs)
		return
	}

	html, err := markdown.Render(req.Markdown)
	if err != nil {
		s.Log.Error("render markdown", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not render markdown")
		return
	}

	id, err := s.Posts.Create(r.Context(), claims.UserID, req.Title, req.Markdown, html)
	if err != nil {
		s.Log.Error("create post", "err", err, "user_id", claims.UserID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not create post")
		return
	}

	post, err := s.Posts.GetByID(r.Context(), id)
	if err != nil {
		s.Log.Error("load created post", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load post")
		return
	}

	s.attachCode(post)
	_ = httpx.WriteJSON(w, http.StatusCreated, post)
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
	if errs := req.Validate(); !errs.IsEmpty() {
		httpx.WriteValidationError(w, errs)
		return
	}

	html, err := markdown.Render(req.Markdown)
	if err != nil {
		s.Log.Error("render markdown", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not render markdown")
		return
	}

	if err := s.Posts.Update(r.Context(), id, req.Title, req.Markdown, html); err != nil {
		if errors.Is(err, postRepo.ErrPostNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
			return
		}

		s.Log.Error("update post", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update post")
		return
	}

	post, err := s.Posts.GetByID(r.Context(), id)
	if err != nil {
		s.Log.Error("load updated post", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load post")
		return
	}

	s.attachCode(post)
	httpx.WriteJSON(w, http.StatusOK, post)
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
		if errors.Is(err, postRepo.ErrPostNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
			return
		}

		s.Log.Error("delete post", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not delete post")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) listPostsForRole(r *http.Request, claims *auth.Claims, limit, offset int) ([]postRepo.Post, error) {
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

func (s *Service) attachCodes(posts []postRepo.Post) {
	if s.Encoder == nil {
		return
	}

	for i := range posts {
		if code, err := s.Encoder.Encode(posts[i].ID); err != nil {
			posts[i].Code = code
		}
	}
}

func (s *Service) attachCode(post *postRepo.Post) {
	if s.Encoder == nil || post == nil {
		return
	}

	if code, err := s.Encoder.Encode(post.ID); err != nil {
		post.Code = code
	}
}
