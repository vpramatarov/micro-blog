// Package shortlinks implements the /api/shortlinks/* (write + handler-filtered list) and /s/{code} (public resolve) HTTP handlers.
package shortlinks

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"text/template"

	"github.com/go-chi/chi/v5"
	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	shortLinksRepo "github.com/vpramatarov/micro-blog/internal/api/repository/shortlinks"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/shortcode"
	"github.com/vpramatarov/micro-blog/internal/validation"
)

const roleAdmin string = "Admin"

var stateTemplate = template.Must(template.New("stateTemplate").Parse(`
<!DOCTYPE HTML>
<html lang="en">
<head>
<meta charset="utf-8">
<title>External Link</title>
</head>
<body>
<h1>You are leaving this site</h1>
<p>This shortened link will take you to external website:</p>
<p><strong>{{ .Host }}</strong></p>
<p>Full URL: <code>{{ .URL }}</code></p>
<p><a href="{{ .URL }}" rel="noopener noreferrer">Continue to {{ .Host }}</a></p>
<p><small>If you did not expect this, close this page.</small></p>
</body>
</html>
`))

type stateTemplateData struct {
	URL  string
	Host string
}

type shortLinkWriteRequest struct {
	OriginalURL string `json:"original_url"`
}

type Service struct {
	ShortLinks *shortLinksRepo.Repo
	Encoder    *shortcode.Encoder
	Log        *slog.Logger
}

func New(shortLinksRepository *shortLinksRepo.Repo, encoder *shortcode.Encoder, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}

	return &Service{ShortLinks: shortLinksRepository, Encoder: encoder, Log: log}
}

// List — GET /api/shortlinks. Any authenticated user. Admins see every row;
// everyone else sees only the rows they own. Subscribers get an empty list (they can't create), which is fine and consistent.
// Paginated via ?page / ?per_page.
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing auth")
		return
	}

	limit, offset, page, perPage, ok := httpx.ParsePagination(w, r)
	if !ok {
		return
	}

	var (
		links []shortLinksRepo.ShortLink
		total int
		err   error
	)
	if claims.Role == roleAdmin {
		if total, err = s.ShortLinks.Count(r.Context()); err == nil {
			links, err = s.ShortLinks.List(r.Context(), limit, offset)
		}
	} else {
		if total, err = s.ShortLinks.CountByUser(r.Context(), claims.UserID); err == nil {
			links, err = s.ShortLinks.ListByUser(r.Context(), claims.UserID, limit, offset)
		}
	}

	if err != nil {
		s.Log.Error("list short links", "err", err, "user_id", claims.UserID, "role", claims.Role)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list short links")
		return
	}

	s.hydrateMany(links)
	httpx.WriteJSON(w, http.StatusOK, httpx.Page[shortLinksRepo.ShortLink]{
		Items: links, Page: page, PerPage: perPage, Total: total,
	})
}

// Create — POST /api/shortlinks. Bouncer enforces shortlink:create.
// user_id comes from the caller's claims so the row's owner is always the authenticated user.
func (s *Service) Create(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing auth")
		return
	}

	var req shortLinkWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "request body is not valid JSON")
		return
	}

	if msg := validation.URL(req.OriginalURL); msg != "" {
		httpx.WriteValidationError(w, map[string]string{"original_url": msg})
		return
	}

	normalizedURL := strings.TrimSpace(req.OriginalURL)
	id, err := s.ShortLinks.Create(r.Context(), claims.UserID, normalizedURL)
	if err != nil {
		s.Log.Error("create short link", "err", err, "user_id", claims.UserID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not create short link")
		return
	}

	link, err := s.ShortLinks.Get(r.Context(), id)
	if err != nil {
		s.Log.Error("load created short link", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load short link")
		return
	}

	s.hydrateOne(link)
	httpx.WriteJSON(w, http.StatusCreated, link)
}

// Update — PUT /api/shortlinks/{id}. Bouncer enforces ownership for non-admins (scope='own'); Admin bypasses to scope='all'.
func (s *Service) Update(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseIDParam(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "invalid short link id")
		return
	}

	var req shortLinkWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "request body is not valid JSON")
		return
	}

	if msg := validation.URL(req.OriginalURL); msg != "" {
		httpx.WriteValidationError(w, map[string]string{"original_url": msg})
		return
	}

	normalizedURL := strings.TrimSpace(req.OriginalURL)
	if err := s.ShortLinks.Update(r.Context(), id, normalizedURL); err != nil {
		if errors.Is(err, shortLinksRepo.ErrShortLinkNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "short link not found")
			return
		}

		s.Log.Error("update short link", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update short link")
		return
	}

	link, err := s.ShortLinks.Get(r.Context(), id)
	if err != nil {
		s.Log.Error("load updated short link", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load short link")
		return
	}

	s.hydrateOne(link)
	httpx.WriteJSON(w, http.StatusOK, link)
}

// Delete — DELETE /api/shortlinks/{id}. Bouncer enforces ownership.
func (s *Service) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseIDParam(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "invalid short link id")
		return
	}

	if err := s.ShortLinks.Delete(r.Context(), id); err != nil {
		if errors.Is(err, shortLinksRepo.ErrShortLinkNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "short link not found")
			return
		}

		s.Log.Error("delete short link", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not delete short link")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Resolve — GET /s/{code}. Fully public. Decodes the hashid, looks
// up the row, and 302-redirects to the original URL. Bad codes and missing
// rows both 404 with the same response so the existence of a given id is not leaked.
func (s *Service) Resolve(w http.ResponseWriter, r *http.Request) {
	if s.Encoder == nil {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "short link not found")
		return
	}

	code := chi.URLParam(r, "code")
	id, err := s.Encoder.Decode(code)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "short link not found")
		return
	}

	link, err := s.ShortLinks.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, shortLinksRepo.ErrShortLinkNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "short link not found")
			return
		}

		s.Log.Error("resolve short link", "err", err, "code", code, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not resolve short link")
		return
	}

	target, err := url.Parse(link.OriginalURL)
	if err != nil || target.Host == "" {
		s.Log.Error("invalid stored short link URL", "err", err, "id", id, "url", link.OriginalURL)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not resolve short link")
		return
	}

	if strings.EqualFold(strings.TrimSpace(r.Host), strings.TrimSpace(target.Host)) {
		http.Redirect(w, r, link.OriginalURL, http.StatusFound)
		return
	}

	s.renderStateTemplate(w, target)
}

func (s *Service) renderStateTemplate(w http.ResponseWriter, target *url.URL) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "no-index, follow")
	w.WriteHeader(http.StatusOK)
	if err := stateTemplate.Execute(w, stateTemplateData{URL: target.String(), Host: target.Host}); err != nil {
		s.Log.Error("render shortlink state template", "err", err)
	}
}

func (s *Service) hydrateMany(links []shortLinksRepo.ShortLink) {
	if s.Encoder == nil {
		return
	}

	for i := range links {
		if code, err := s.Encoder.Encode(links[i].ID); err == nil {
			links[i].Code = code
		}
	}
}

func (s *Service) hydrateOne(link *shortLinksRepo.ShortLink) {
	if s.Encoder == nil || link == nil {
		return
	}

	if code, err := s.Encoder.Encode(link.ID); err == nil {
		link.Code = code
	}
}
