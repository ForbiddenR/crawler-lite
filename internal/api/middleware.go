package api

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yourteam/crawler-lite/internal/api/render"
	"github.com/yourteam/crawler-lite/internal/auth"
)

// ctxKeyClaims is the gin.Context key under which verified claims are stored.
const ctxKeyClaims = "auth.claims"

// claimsFrom returns the auth claims attached by authMiddleware, or nil.
func claimsFrom(c *gin.Context) *auth.Claims {
	v, ok := c.Get(ctxKeyClaims)
	if !ok {
		return nil
	}
	claims, _ := v.(*auth.Claims)
	return claims
}

// authMiddleware verifies a Bearer token and attaches its claims to the gin
// context. Handlers behind this middleware can rely on claimsFrom being non-nil.
func authMiddleware(svc *auth.Service, _ *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			render.Error(c, http.StatusUnauthorized, "missing bearer token")
			return
		}
		tok := strings.TrimPrefix(h, "Bearer ")
		claims, err := svc.VerifyToken(tok)
		if err != nil {
			render.Error(c, http.StatusUnauthorized, "invalid token")
			return
		}
		c.Set(ctxKeyClaims, &claims)
		c.Next()
	}
}

// slogLogger emits one structured log line per request with duration and status.
func slogLogger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Info("http",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"bytes", c.Writer.Size(),
			"dur_ms", time.Since(start).Milliseconds(),
		)
	}
}

// slogRecoverer turns a panic into a 500 and logs the stack.
func slogRecoverer(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("panic",
					"path", c.Request.URL.Path,
					"recover", rec,
					"stack", string(debug.Stack()),
				)
				render.Error(c, http.StatusInternalServerError, "internal error")
			}
		}()
		c.Next()
	}
}

// corsMiddleware: dev-friendly. Allows any origin; tighten for production.
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
