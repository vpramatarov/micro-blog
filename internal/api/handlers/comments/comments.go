// Package comments implements:
// POST /api/posts/{id}/comments (create),
// DELETE /api/comments/{id} (moderation delete), and the public
// GET /posts/{slug}/comments (paginated list).
//
// None of these routes go through the Bouncer matrix. Create is open to every authenticated role, so a permission lookup could never deny;
// delete is granted to the parent post's author (an ownership relation one table removed from the resource)
// plus the flat Editor/Admin override, which the single-OwnerKind own/all/none scope model cannot express.
// Authorization is therefore handler-level - same precedent as /api/me and the RequireEditorOrAdmin groups.
package comments

import (
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	commentsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/comments"
	postsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/posts"
	settingsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/settings"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/validation"
)

const (
	roleAdmin  = "Admin"
	roleEditor = "Editor"
)

// Service handles the comment endpoints.
// Posts backs the published gate and the post-author lookup for delete authorization; Settings backs the global comments kill-switch.
type Service struct {
	Comments *commentsrepo.Repo
	Posts    *postsrepo.Repo
	Settings *settingsrepo.Repo
	Log      *slog.Logger
}

func New(commentsRepo *commentsrepo.Repo, postsRepo *postsrepo.Repo, settingsRepo *settingsrepo.Repo, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}

	return &Service{Comments: commentsRepo, Posts: postsRepo, Settings: settingsRepo, Log: log}
}

type commentCreateRequest struct {
	Content string `json:"content"`
	// ParentID 0 / omitted = top-level comment; non-zero = reply to that top-level comment (single-level threading - a reply can't be replied to).
	ParentID int64 `json:"parent_id"`
}

// CommentResponse is a top-level comment plus its single-level replies.
// Replies is always allocated so a reply-less comment serializes as "replies":[] rather than null - same convention as PostResponse.Tags.
type CommentResponse struct {
	commentsrepo.Comment
	Replies []commentsrepo.Comment `json:"replies"`
}

// Create - POST /api/posts/{id}/comments.
// Any authenticated role on published posts and only while commenting is enabled both globally (settings kill-switch) and on the post itself - the toggle gate applies to
// every role including Admin. user_id comes from the caller's claims so the
// row's owner is always the authenticated user.
func (s *Service) Create(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing auth")
		return
	}

	postID, err := parseIDParam(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "invalid post id")
		return
	}

	var req commentCreateRequest
	if err := json.UnmarshalRead(r.Body, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "request body is not valid JSON")
		return
	}
	errs := validation.New()
	errs.Add("content", validation.CommentContent(req.Content))
	if req.ParentID < 0 {
		errs.Add("parent_id", "must be a positive id")
	}

	if !errs.IsEmpty() {
		httpx.WriteValidationError(w, errs)
		return
	}

	// Non-published posts are indistinguishable from missing ones - same 404 as the public read paths, so draft ids can't be probed by commenters.
	post, err := s.Posts.GetByID(r.Context(), postID)
	if err != nil {
		if errors.Is(err, postsrepo.ErrPostNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
			return
		}

		s.Log.Error("get post for comment", "err", err, "post_id", postID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not create comment")
		return
	}

	if post.Status != postsrepo.PostStatusPublished {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
		return
	}

	// Toggle gate - global kill-switch first, then the per-post flag.
	// One envelope for both levels; there's nothing actionable in the difference for a commenter, and moderators can see both toggles anyway.
	global, err := s.Settings.CommentsEnabled(r.Context())
	if err != nil {
		s.Log.Error("read comments kill-switch", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not create comment")
		return
	}

	if !global || !post.CommentsEnabled {
		httpx.WriteError(w, http.StatusForbidden, "comments_disabled", "comments are disabled for this post")
		return
	}

	// Reply policy: the parent must exist, sit on the same post, and itself be top-level.
	// Violations are validation errors on parent_id - same envelope as the posts handler's category/tag existence checks.
	if req.ParentID != 0 {
		parent, err := s.Comments.Get(r.Context(), req.ParentID)
		switch {
		case errors.Is(err, commentsrepo.ErrCommentNotFound):
			httpx.WriteValidationError(w, map[string]string{"parent_id": "does not exist"})
			return
		case err != nil:
			s.Log.Error("get parent comment", "err", err, "parent_id", req.ParentID)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not create comment")
			return
		case parent.PostID != postID:
			httpx.WriteValidationError(w, map[string]string{"parent_id": "must belong to the same post"})
			return
		case parent.ParentID != 0:
			httpx.WriteValidationError(w, map[string]string{"parent_id": "must be a top-level comment"})
			return
		}
	}

	content := strings.TrimSpace(req.Content)
	id, err := s.Comments.Create(r.Context(), postID, claims.UserID, req.ParentID, content)
	if err != nil {
		s.Log.Error("create comment", "err", err, "post_id", postID, "user_id", claims.UserID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not create comment")
		return
	}

	created, err := s.Comments.Get(r.Context(), id)
	if err != nil {
		s.Log.Error("load created comment", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load comment")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, CommentResponse{
		Comment: *created,
		Replies: make([]commentsrepo.Comment, 0),
	})
}

// ListBySlug - GET /posts/{slug}/comments. Public. Returns the page of TOP-LEVEL comments oldest-first, each with its replies embedded (one batched round-trip).
// `total` counts top-level comments only - it drives the page math and is deliberately NOT the post's comment_count, which includes replies.
// Comments stay readable while commenting is disabled (read-only mode); only non-published posts 404.
func (s *Service) ListBySlug(w http.ResponseWriter, r *http.Request) {
	slugParam := chi.URLParam(r, "slug")
	post, err := s.Posts.GetBySlug(r.Context(), slugParam)
	if err != nil {
		if errors.Is(err, postsrepo.ErrPostNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
			return
		}

		s.Log.Error("get post for comments", "err", err, "slug", slugParam)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list comments")
		return
	}

	// Mirror the public post reads' status gate so a draft post's comments can't be read through a guessed slug.
	if post.Status != postsrepo.PostStatusPublished {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "post not found")
		return
	}

	limit, offset, page, perPage, ok := httpx.ParsePagination(w, r)
	if !ok {
		return
	}

	total, err := s.Comments.CountTopLevel(r.Context(), post.ID)
	if err != nil {
		s.Log.Error("count comments", "err", err, "post_id", post.ID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list comments")
		return
	}

	topLevel, err := s.Comments.ListTopLevel(r.Context(), post.ID, limit, offset)
	if err != nil {
		s.Log.Error("list comments", "err", err, "post_id", post.ID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list comments")
		return
	}

	parentIDs := make([]int64, len(topLevel))
	for i := range topLevel {
		parentIDs[i] = topLevel[i].ID
	}

	repliesByParent, err := s.Comments.ListReplies(r.Context(), parentIDs)
	if err != nil {
		s.Log.Error("list replies", "err", err, "post_id", post.ID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list comments")
		return
	}

	items := make([]CommentResponse, len(topLevel))
	for i := range topLevel {
		c := topLevel[i]
		replies := repliesByParent[c.ID]
		if replies == nil {
			replies = make([]commentsrepo.Comment, 0)
		}

		items[i] = CommentResponse{Comment: c, Replies: replies}
	}

	httpx.WriteJSON(w, http.StatusOK, httpx.Page[CommentResponse]{
		Items: items, Page: page, PerPage: perPage, Total: total,
	})
}

// Delete - DELETE /api/comments/{id}. Allowed for the parent post's author, Editor, and Admin - deliberately NOT for the comment's own author
// (product call: commenters can't retract, moderators curate). Works even while commenting is disabled: moderation is never frozen. Replies cascade at the DB.
func (s *Service) Delete(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing auth")
		return
	}

	id, err := parseIDParam(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "invalid comment id")
		return
	}

	comment, err := s.Comments.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, commentsrepo.ErrCommentNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "comment not found")
			return
		}

		s.Log.Error("get comment for delete", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not delete comment")
		return
	}

	// Cheapest checks first - only the post-author lookup costs a query.
	allowed := claims.Role == roleAdmin || claims.Role == roleEditor
	if !allowed {
		ownerID, err := s.Posts.GetOwnerID(r.Context(), comment.PostID)
		if err != nil {
			// ErrPostNotFound is impossible while the comment row exists (FK
			// CASCADE), so any error here is internal.
			s.Log.Error("get post owner for comment delete", "err", err, "post_id", comment.PostID)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not delete comment")
			return
		}

		allowed = ownerID == claims.UserID
	}

	if !allowed {
		httpx.WriteError(w, http.StatusForbidden, "forbidden", "you cannot delete this comment")
		return
	}

	if err := s.Comments.Delete(r.Context(), id); err != nil {
		if errors.Is(err, commentsrepo.ErrCommentNotFound) {
			// Lost a race with a concurrent delete.
			httpx.WriteError(w, http.StatusNotFound, "not_found", "comment not found")
			return
		}

		s.Log.Error("delete comment", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not delete comment")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func parseIDParam(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}
