// Package settings implements the /admin/settings/* endpoints - currently
// just the global comments kill-switch. Role gating (Admin + Editor) is
// entirely the router group's RequireEditorOrAdmin; there is no ownership
// concept, so the Bouncer matrix is not involved (same reasoning as the
// categories/tags write groups).
package settings

import (
	"encoding/json/v2"
	"log/slog"
	"net/http"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	settingsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/settings"
)

// Service handles the runtime-settings endpoints.
type Service struct {
	Settings *settingsrepo.Repo
	Log      *slog.Logger
}

func New(settingsRepo *settingsrepo.Repo, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{Settings: settingsRepo, Log: log}
}

// commentsSetting is both the PUT request body and the GET/PUT response:
// {"enabled": bool}.
type commentsSetting struct {
	Enabled bool `json:"enabled"`
}

// GetCommentsSetting - GET /admin/settings/comments. Admin + Editor. Reports
// the global kill-switch; the per-post flag lives on each post row.
func (s *Service) GetCommentsSetting(w http.ResponseWriter, r *http.Request) {
	enabled, err := s.Settings.CommentsEnabled(r.Context())
	if err != nil {
		// Includes a missing row - migration 00011 seeds it, so that's an
		// internal inconsistency, not a client-visible 404.
		s.Log.Error("read comments setting", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not read comments setting")
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, commentsSetting{Enabled: enabled})
}

// UpdateCommentsSetting - PUT /admin/settings/comments. Admin + Editor.
// Disabling closes commenting on EVERY post for every role (including the
// caller); per-post comments_enabled flags keep their values and take effect
// again when the switch returns to enabled.
func (s *Service) UpdateCommentsSetting(w http.ResponseWriter, r *http.Request) {
	var req commentsSetting
	if err := json.UnmarshalRead(r.Body, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "request body is not valid JSON")
		return
	}
	if err := s.Settings.SetCommentsEnabled(r.Context(), req.Enabled); err != nil {
		s.Log.Error("update comments setting", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update comments setting")
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, commentsSetting{Enabled: req.Enabled})
}
