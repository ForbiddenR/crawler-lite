// Package auth holds the HTTP handlers for /api/auth/*.
package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/yourteam/crawler-lite/internal/api/render"
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

func (h *Handler) Login(c *gin.Context) {
	var req loginReq
	if !render.Decode(c, &req) {
		return
	}
	if req.Email == "" || req.Password == "" {
		render.Error(c, http.StatusBadRequest, "email and password required")
		return
	}
	tok, u, err := h.svc.Login(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, authsvc.ErrInvalidCredentials):
			render.Error(c, http.StatusUnauthorized, "invalid credentials")
		case errors.Is(err, authsvc.ErrInactive):
			render.Error(c, http.StatusForbidden, "account inactive")
		default:
			h.log.Error("login error", "err", err)
			render.Error(c, http.StatusInternalServerError, "login failed")
		}
		return
	}
	render.JSON(c, http.StatusOK, loginResp{
		Token: tok,
		User: userResponse{
			ID: u.ID, Email: u.Email, Role: u.Role, Name: u.DisplayName,
		},
	})
}

// Me returns the current user's profile. The Authorization Bearer token has
// already been verified by api.authMiddleware; we re-fetch the user so a
// recent deactivation takes effect immediately.
func (h *Handler) Me(c *gin.Context) {
	tok := tokenFromHeader(c)
	if tok == "" {
		render.Error(c, http.StatusUnauthorized, "missing token")
		return
	}
	u, err := h.svc.CurrentUser(c.Request.Context(), tok)
	if err != nil {
		render.Error(c, http.StatusUnauthorized, "user not found")
		return
	}
	render.JSON(c, http.StatusOK, userResponse{
		ID: u.ID, Email: u.Email, Role: u.Role, Name: u.DisplayName,
	})
}

func tokenFromHeader(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(h, "Bearer ")
}
