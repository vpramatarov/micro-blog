// Package posts implements the /posts/* (public reads) and /admin/posts/*
// (writes + role-aware list) HTTP handlers. Public reads use hashid codes;
// admin operations use raw numeric ids.
package posts

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

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
	Title               string  `json:"title"`
	Markdown            string  `json:"markdown_content"`
	CategoryID          int64   `json:"category_id"`
	TagIDs              []int64 `json:"tag_ids"`
	RemoveFeaturedImage bool    `json:"remove_featured_image"`
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

	req, fileHeader, ok := s.parsePostMultipart(w, r)
	if !ok {
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

	// Image is validated AND saved BEFORE the DB INSERT so any failure (bad format, too small, disk full) surfaces as a clean 4xx with no post row to clean up.
	// The reverse — INSERT first, file last — would leave orphaned rows on every image-validation error.
	var imagePath string
	if fileHeader != nil {
		encoded, format, ext, imgOK := s.readAndValidateImage(w, fileHeader)
		if !imgOK {
			return
		}

		imagePath, imgOK = s.saveImageAndEnqueue(w, r, fileHeader, encoded, format, ext)
		if !imgOK {
			return
		}
	}

	id, err := s.createWithSlug(r, postRepository.PostInsert{
		AuthorID: claims.UserID, CategoryID: req.CategoryID,
		Title: req.Title, Markdown: req.Markdown, HTML: html,
		Slug: base, FeaturedImagePath: imagePath,
	})
	if err != nil {
		// Roll the image back so we don't leak orphans on a failed INSERT.
		if imagePath != "" {
			err := s.Storage.DeleteAll(imagePath)
			if err != nil {
				// just log for now.
				s.Log.Warn("rollback image", "err", err, "image_path", imagePath)
			}
		}

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

	req, fileHeader, ok := s.parsePostMultipart(w, r)
	if !ok {
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

	if err := s.updateWithSlug(r, id, postRepository.PostUpdate{
		CategoryID: req.CategoryID, Title: req.Title, Markdown: req.Markdown, HTML: html, Slug: base, FeaturedImagePath: newPath,
	}); err != nil {
		// Roll back any freshly-saved image on UPDATE failure so we don't leak orphans.
		// We deliberately do NOT touch oldPath here — the existing post still references it.
		if newPath != "" && newPath != oldPath {
			_ = s.Storage.DeleteAll(newPath)
		}

		if errors.Is(err, postRepository.ErrPostNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
			return
		}

		s.Log.Error("update post", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update post")
		return
	}

	// Tag set is rewritten unconditionally on update — pass nil to clear.
	if err := s.Tags.ReplaceForPost(r.Context(), id, req.TagIDs); err != nil {
		s.Log.Error("replace tags", "err", err, "post_id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update tags")
		return
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
// Returns (request, fileHeader, "", "", nil) on success when no file was uploaded;
// (request, header, "", "", nil) when a file IS present.
// The caller is responsible for reading + validating the file via header.Open().
func (s *Service) parsePostMultipart(w http.ResponseWriter, r *http.Request) (postWriteRequest, *multipart.FileHeader, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, MultipartBodyLimit)
	if err := r.ParseMultipartForm(MultipartBodyLimit); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds 5 MB limit")
			return postWriteRequest{}, nil, false
		}

		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "request body is not valid multipart/form-data")
		return postWriteRequest{}, nil, false
	}

	rawData := r.FormValue("data")
	if rawData == "" {
		httpx.WriteValidationError(w, map[string]string{"data": "is required (JSON-encoded post fields)"})
		return postWriteRequest{}, nil, false
	}

	var req postWriteRequest
	if err := json.Unmarshal([]byte(rawData), &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body",
			"\"data\" form field is not valid JSON")
		return postWriteRequest{}, nil, false
	}

	// Optional file part.
	var header *multipart.FileHeader
	if r.MultipartForm != nil {
		if files, ok := r.MultipartForm.File["featured_image"]; ok && len(files) > 0 {
			header = files[0]
		}
	}

	return req, header, true
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

	data, err := io.ReadAll(src)
	if err != nil {
		s.Log.Error("read featured image", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not read uploaded file")
		return nil, "", "", false
	}

	img, format, ext, err := imagex.ValidateAndDecode(data)
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
