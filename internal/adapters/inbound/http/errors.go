package http

import (
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"floodnow-api/internal/domain/apperr"
)

func statusForCode(code apperr.Code) int {
	switch code {
	case apperr.CodeValidation:
		return http.StatusBadRequest
	case apperr.CodeNotFound:
		return http.StatusNotFound
	case apperr.CodeConflict:
		return http.StatusConflict
	case apperr.CodePayloadTooLarge:
		return http.StatusRequestEntityTooLarge
	case apperr.CodeUnavailable:
		return http.StatusServiceUnavailable
	case apperr.CodeUnauthorized:
		return http.StatusUnauthorized
	case apperr.CodeRateLimited:
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

// writeError maps any error to the documented JSON error envelope. Only
// *apperr.Error carries a safe message/code to the client; anything else is
// logged server-side and returned as an opaque 500.
func writeError(c *gin.Context, err error) {
	var appErr *apperr.Error
	if errors.As(err, &appErr) {
		c.JSON(statusForCode(appErr.Code), errorEnvelope{Error: errorBody{
			Code:    string(appErr.Code),
			Message: appErr.Message,
			Fields:  appErr.Fields,
		}})
		return
	}

	log.Printf("internal error on %s %s: %v", c.Request.Method, c.Request.URL.Path, err)
	c.JSON(http.StatusInternalServerError, errorEnvelope{Error: errorBody{
		Code:    string(apperr.CodeInternal),
		Message: "internal server error",
	}})
}
