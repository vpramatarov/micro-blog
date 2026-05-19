// Package categories implements the public GET /categories list endpoint and
// the Admin/Editor-only /admin/categories CRUD endpoints.
package categories

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	categoriesRepo "github.com/vpramatarov/micro-blog/internal/api/repository/categories"
	"github.com/vpramatarov/micro-blog/internal/validation"
)

// Service handles category endpoints. The only repo dependency is the categories table — categories live on their own;
// the posts.category_id FK is enforced by SQLite, not from here.
type Service struct {
	Categories *categoriesRepo.Repo
	Log        *slog.Logger
}

func New(categoriesRepo *categoriesRepo.Repo, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}

	return &Service{Categories: categoriesRepo, Log: log}
}

type categoryWriteRequest struct {
	Name string `json:"name"`
}

// List — GET /categories. Public. Paginated via ?page / ?per_page.
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	limit, offset, page, perPage, ok := httpx.ParsePagination(w, r)
	if !ok {
		return
	}

	total, err := s.Categories.Count(r.Context())
	if err != nil {
		s.Log.Error("count categories", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list categories")
		return
	}

	cats, err := s.Categories.List(r.Context(), limit, offset)
	if err != nil {
		s.Log.Error("list categories", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list categories")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, httpx.Page[categoriesRepo.Category]{
		Items: cats, Page: page, PerPage: perPage, Total: total,
	})
}

// Create — POST /admin/categories. Admin or Editor only (enforced by router-level RequireEditorOrAdmin middleware).
func (s *Service) Create(w http.ResponseWriter, r *http.Request) {
	var req categoryWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "request body is not valid JSON")
		return
	}

	if msg := validation.Name(req.Name); msg != "" {
		httpx.WriteValidationError(w, map[string]string{"name": msg})
		return
	}
	name := strings.TrimSpace(req.Name)

	id, err := s.Categories.Create(r.Context(), name)
	if err != nil {
		if errors.Is(err, categoriesRepo.ErrCategoryDuplicate) {
			httpx.WriteError(w, http.StatusConflict, "duplicate", "category with this name already exists")
			return
		}

		s.Log.Error("create category", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not create category")
		return
	}

	cat, err := s.Categories.GetByID(r.Context(), id)
	if err != nil {
		s.Log.Error("load created category", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load category")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, cat)
}

// Update — PUT /admin/categories/{id}. Admin or Editor only.
func (s *Service) Update(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseIDParam(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "invalid category id")
		return
	}

	var req categoryWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "request body is not valid JSON")
		return
	}

	if msg := validation.Name(req.Name); msg != "" {
		httpx.WriteValidationError(w, map[string]string{"name": msg})
		return
	}

	name := strings.TrimSpace(req.Name)

	if err := s.Categories.Update(r.Context(), id, name); err != nil {
		switch {
		case errors.Is(err, categoriesRepo.ErrCategoryNotFound):
			httpx.WriteError(w, http.StatusNotFound, "not_found", "category not found")
		case errors.Is(err, categoriesRepo.ErrCategoryDuplicate):
			httpx.WriteError(w, http.StatusConflict, "duplicate", "category with this name already exists")
		default:
			s.Log.Error("update category", "err", err, "id", id)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update category")
		}
		return
	}

	cat, err := s.Categories.GetByID(r.Context(), id)
	if err != nil {
		s.Log.Error("load updated category", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load category")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, cat)
}

// Delete — DELETE /admin/categories/{id}. Admin or Editor only.
// Returns 409 category_in_use when any post still references the row; the FK is ON DELETE RESTRICT.
func (s *Service) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseIDParam(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "invalid category id")
		return
	}

	if err := s.Categories.Delete(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, categoriesRepo.ErrCategoryNotFound):
			httpx.WriteError(w, http.StatusNotFound, "not_found", "category not found")
		case errors.Is(err, categoriesRepo.ErrCategoryInUse):
			httpx.WriteError(w, http.StatusConflict, "category_in_use", "category is referenced by one or more posts")
		default:
			s.Log.Error("delete category", "err", err, "id", id)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not delete category")
		}

		return
	}

	w.WriteHeader(http.StatusNoContent)
}
