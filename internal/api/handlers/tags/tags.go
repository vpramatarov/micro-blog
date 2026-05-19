// Package tags implements the public GET /tags list endpoint and the Admin/Editor-only /admin/tags CRUD endpoints.
package tags

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	tagsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/tags"
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

	if msg := validation.Name(req.Name); msg != "" {
		httpx.WriteValidationError(w, map[string]string{"name": msg})
		return
	}

	name := strings.TrimSpace(req.Name)
	id, err := s.Tags.Create(r.Context(), name)
	if err != nil {
		if errors.Is(err, tagsrepo.ErrTagDuplicate) {
			httpx.WriteError(w, http.StatusConflict, "duplicate", "tag with this name already exists")
			return
		}

		s.Log.Error("create tag", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not create tag")
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

	if msg := validation.Name(req.Name); msg != "" {
		httpx.WriteValidationError(w, map[string]string{"name": msg})
		return
	}

	name := strings.TrimSpace(req.Name)
	if err := s.Tags.Update(r.Context(), id, name); err != nil {
		switch {
		case errors.Is(err, tagsrepo.ErrTagNotFound):
			httpx.WriteError(w, http.StatusNotFound, "not_found", "tag not found")
		case errors.Is(err, tagsrepo.ErrTagDuplicate):
			httpx.WriteError(w, http.StatusConflict, "duplicate", "tag with this name already exists")
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
