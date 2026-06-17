// Package render centralizes HTTP response shaping so every handler emits the
// same JSON envelope. The error shape ({"error":{"code","message"}}) matches
// what the web client parses in web/src/api/client.ts.
package render

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type errEnvelope struct {
	Error errBody `json:"error"`
}

type errBody struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// JSON writes v as JSON with the given status.
func JSON(c *gin.Context, status int, v any) {
	c.JSON(status, v)
}

// Error writes the standard error envelope and aborts the handler chain.
func Error(c *gin.Context, status int, message string) {
	c.AbortWithStatusJSON(status, errEnvelope{Error: errBody{Message: message}})
}

// Decode binds the JSON request body into dst. On failure it writes a 400 and
// returns false, so callers can `if !render.Decode(c, &req) { return }`.
func Decode(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}
