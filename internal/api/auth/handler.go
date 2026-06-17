// Package auth holds the HTTP handlers for /api/auth/*.
package auth

import (
	"errors"
	"log/slog"
	"net/http"

	authsvc "github.com/yourteam/crawler-lite/internal/auth"
)

type Handler struct {
	svc *authsvc.Service
	log *slog.Logger
}

func NewHandler(svc *authsvc.Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResp struct {
	Token string       `json:"token"`
	User  userResponse `json:"user"`
}

type userResponse struct {
	ID    int64        `json:"id"`
	Email string       `json:"email"`
	Role  authsvc.Role `json:"role"`
	Name  string       `json:"display_name,omitempty"`
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if !decode(w, r, &req) {
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password required")
		return
	}
	tok, u, err := h.svc.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, authsvc.ErrInvalidCredentials):
			writeError(w, http.StatusUnauthorized, "invalid credentials")
		case errors.Is(err, authsvc.ErrInactive):
			writeError(w, http.StatusForbidden, "account inactive")
		default:
			h.log.Error("login error", "err", err)
			writeError(w, http.StatusInternalServerError, "login failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, loginResp{
		Token: tok,
		User: userResponse{
			ID: u.ID, Email: u.Email, Role: u.Role, Name: u.DisplayName,
		},
	})
}

// Me returns the current user's profile. The Authorization Bearer token has
// already been verified by api.authMiddleware; we re-fetch the user so a
// recent deactivation takes effect immediately.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	tok := tokenFromRequest(r)
	if tok == "" {
		writeError(w, http.StatusUnauthorized, "missing token")
		return
	}
	u, err := h.svc.CurrentUser(r.Context(), tok)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, userResponse{
		ID: u.ID, Email: u.Email, Role: u.Role, Name: u.DisplayName,
	})
}
