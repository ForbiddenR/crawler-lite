package api

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/yourteam/crawler-lite/internal/auth"
)

type ctxKey int

const (
	ctxKeyClaims ctxKey = iota + 1
)

// claimsFrom returns the auth claims attached by authMiddleware, or nil.
func claimsFrom(ctx context.Context) *auth.Claims {
	v, _ := ctx.Value(ctxKeyClaims).(*auth.Claims)
	return v
}

// authMiddleware verifies a Bearer token and attaches its claims to the request
// context. Endpoints under r.Group with this middleware can rely on claims
// being present.
func authMiddleware(svc *auth.Service, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				writeError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			tok := strings.TrimPrefix(h, "Bearer ")
			claims, err := svc.VerifyToken(tok)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid token")
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyClaims, &claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// slogLogger replaces chi's default text logger with a slog one-liner per
// request, including duration and status code.
func slogLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r)
			log.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"dur_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

func slogRecoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic",
						"path", r.URL.Path,
						"recover", rec,
						"stack", string(debug.Stack()),
					)
					writeError(w, http.StatusInternalServerError, "internal error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// corsMiddleware: dev-friendly. Allows any origin; tighten for production.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
