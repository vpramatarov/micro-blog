// Package tags implements the public GET /tags list endpoint and the Admin/Editor-only /admin/tags CRUD endpoints.
package tags

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	tagsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/tags"
	"github.com/vpramatarov/micro-blog/internal/slug"
	"github.com/vpramatarov/micro-blog/internal/validation"
)

// Service handles tag endpoints. post_tags rows that reference deleted tags CASCADE away — there is no in-use branch on delete.
type Service struct {
	Tags *tagsrepo.Repo
	Log  *slog.Logger
}

func New(tagsRepo *tagsrepo.Repo, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}

	return &Service{Tags: tagsRepo, Log: log}
}

type tagWriteRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug,omitempty"`
}

func (r *tagWriteRequest) normalize() {
	r.Name = strings.TrimSpace(r.Name)
	r.Slug = strings.TrimSpace(r.Slug)
}

func (r *tagWriteRequest) Validate() validation.Errors {
	e := validation.New()
	e.Add("name", validation.Name(r.Name))

	if r.Slug != "" {
		e.Add("slug", validation.Slug(r.Slug))
	}

	return e
}

// List — GET /tags. Public. Paginated via ?page / ?per_page.
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	limit, offset, page, perPage, ok := httpx.ParsePagination(w, r)
	if !ok {
		return
	}

	total, err := s.Tags.Count(r.Context())
	if err != nil {
		s.Log.Error("count tags", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list tags")
		return
	}

	ts, err := s.Tags.List(r.Context(), limit, offset)
	if err != nil {
		s.Log.Error("list tags", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list tags")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, httpx.Page[tagsrepo.Tag]{
		Items: ts, Page: page, PerPage: perPage, Total: total,
	})
}

// Create— POST /admin/tags. Admin or Editor only.
func (s *Service) Create(w http.ResponseWriter, r *http.Request) {
	var req tagWriteRequest
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

	id, err := slug.Allocate(r.Context(), s.Tags, req.Name, req.Slug, 0, s.createFn(r.Context(), req.Name))
	if err != nil {
		switch {
		case errors.Is(err, slug.ErrEmptyGeneratedSlug):
			httpx.WriteValidationError(w, map[string]string{"name": "must contain at least one Latin or Cyrillic letter or digit"})
		case errors.Is(err, tagsrepo.ErrTagDuplicate):
			httpx.WriteError(w, http.StatusConflict, "duplicate", "tag with this name already exists")
		case errors.Is(err, slug.ErrDuplicate):
			httpx.WriteError(w, http.StatusConflict, "slug_conflict", "tag with this slug already exists")
		default:
			s.Log.Error("create tag", "err", err)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not create tag")
		}
		return
	}

	tag, err := s.Tags.GetByID(r.Context(), id)
	if err != nil {
		s.Log.Error("load created tag", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load tag")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, tag)
}

// Update — PUT /admin/tags/{id}. Admin or Editor only.
func (s *Service) Update(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseIDParam(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "invalid tag id")
		return
	}

	var req tagWriteRequest
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

	existing, err := s.Tags.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, tagsrepo.ErrTagNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "tag not found")
			return
		}

		s.Log.Error("get tag for update", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update tag")
		return
	}

	desiredSlug := req.Slug
	if desiredSlug == "" {
		desiredSlug = existing.Slug
	}

	_, err = slug.Allocate(r.Context(), s.Tags, req.Name, desiredSlug, id, s.updateFn(r.Context(), id, req.Name))
	if err != nil {
		switch {
		case errors.Is(err, tagsrepo.ErrTagNotFound):
			httpx.WriteError(w, http.StatusNotFound, "not_found", "tag not found")
		case errors.Is(err, tagsrepo.ErrTagDuplicate):
			httpx.WriteError(w, http.StatusConflict, "duplicate", "tag with this name already exists")
		case errors.Is(err, slug.ErrDuplicate):
			httpx.WriteError(w, http.StatusConflict, "slug_conflict", "tag with this slug already exists")
		default:
			s.Log.Error("update tag", "err", err, "id", id)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update tag")
		}
		return
	}

	tag, err := s.Tags.GetByID(r.Context(), id)
	if err != nil {
		s.Log.Error("load updated tag", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load tag")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, tag)
}

// Delete — DELETE /admin/tags/{id}. Admin or Editor only. Cascades to
// post_tags so callers don't need to detach first.
func (s *Service) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseIDParam(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "invalid tag id")
		return
	}

	if err := s.Tags.Delete(r.Context(), id); err != nil {
		if errors.Is(err, tagsrepo.ErrTagNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "tag not found")
			return
		}

		s.Log.Error("delete tag", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not delete tag")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) createFn(ctx context.Context, name string) func(string) (int64, error) {
	return func(slugCandidate string) (int64, error) {
		return s.Tags.Create(ctx, name, slugCandidate)
	}
}

func (s *Service) updateFn(ctx context.Context, id int64, name string) func(string) (struct{}, error) {
	return func(slugCandidate string) (struct{}, error) {
		return struct{}{}, s.Tags.Update(ctx, id, name, slugCandidate)
	}
}
