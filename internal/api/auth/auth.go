// Package auth registers the HTTP handlers for /api/auth/*.
//
// Style: free functions + closures. Handlers are stateless — they delegate to
// authsvc.Service, which owns the database. There is no Handler struct.
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

// RegisterPublicRoutes mounts POST /auth/login on the unauthenticated api group.
func RegisterPublicRoutes(g gin.IRoutes, svc *authsvc.Service, log *slog.Logger) {
	g.POST("/auth/login", func(c *gin.Context) {
		var req loginReq
		if !render.Decode(c, &req) {
			return
		}
		if req.Email == "" || req.Password == "" {
			render.Error(c, http.StatusBadRequest, "email and password required")
			return
		}
		tok, u, err := svc.Login(c.Request.Context(), req.Email, req.Password)
		if err != nil {
			switch {
			case errors.Is(err, authsvc.ErrInvalidCredentials):
				render.Error(c, http.StatusUnauthorized, "invalid credentials")
			case errors.Is(err, authsvc.ErrInactive):
				render.Error(c, http.StatusForbidden, "account inactive")
			default:
				log.Error("login error", "err", err)
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
	})
}

// RegisterProtectedRoutes mounts GET /auth/me on a group already wrapped in
// authMiddleware. We re-fetch the user from the DB so a recent deactivation
// takes effect immediately.
//
// log is unused today but kept for future error logging — the failure modes
// here are all "user not found" / "missing token", which return 401 without
// server-side logging.
func RegisterProtectedRoutes(g gin.IRoutes, svc *authsvc.Service, log *slog.Logger) {
	_ = log
	g.GET("/auth/me", func(c *gin.Context) {
		tok := tokenFromHeader(c)
		if tok == "" {
			render.Error(c, http.StatusUnauthorized, "missing token")
			return
		}
		u, err := svc.CurrentUser(c.Request.Context(), tok)
		if err != nil {
			render.Error(c, http.StatusUnauthorized, "user not found")
			return
		}
		render.JSON(c, http.StatusOK, userResponse{
			ID: u.ID, Email: u.Email, Role: u.Role, Name: u.DisplayName,
		})
	})
}

func tokenFromHeader(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(h, "Bearer ")
}
