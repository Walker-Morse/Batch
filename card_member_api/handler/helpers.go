package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/walker-morse/batch/card_member_api/handler/middleware"
	"github.com/walker-morse/batch/card_member_api/ports"
)

// errorResponse is the standard error body.
type errorResponse struct {
	Error string `json:"error"`
}

// WriteJSON writes a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes a standard error response.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, errorResponse{Error: msg})
}

// MapServiceError maps domain sentinel errors to HTTP status codes.
func MapServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ports.ErrMemberNotFound), errors.Is(err, ports.ErrCardNotFound):
		WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ports.ErrMemberNotResolved):
		WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ports.ErrDuplicateRequest):
		WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ports.ErrAlreadyCancelled):
		WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ports.ErrInvalidRequest):
		WriteError(w, http.StatusBadRequest, err.Error())
	default:
		WriteError(w, http.StatusInternalServerError, "internal server error")
	}
}

// BuildRequestContext extracts JWT claims from middleware and parses the
// X-Correlation-ID header into a RequestContext.
func BuildRequestContext(r *http.Request) (ports.RequestContext, bool) {
	claims, ok := middleware.ClaimsFromContext(r.Context())
	if !ok {
		return ports.RequestContext{}, false
	}
	corrIDStr := r.Header.Get("X-Correlation-ID")
	corrID, err := uuid.Parse(corrIDStr)
	if err != nil {
		corrID = uuid.New()
	}
	return ports.RequestContext{
		CorrelationID:   corrID,
		TenantID:        claims.TenantID,
		CallerType:      ports.CallerType(claims.CallerType),
		SubjectMemberID: claims.SubjectMemberID,
	}, true
}

// PathParam extracts a named segment from a URL path.
// Handles both chi (/members/{id}) and stdlib patterns (/members/{id}).
// Stdlib 1.22+ supports r.PathValue("id").
func PathParam(r *http.Request, name string) string {
	// Go 1.22+ net/http pattern matching
	if v := r.PathValue(name); v != "" {
		return v
	}
	// Fallback: parse from URL path manually for older routers
	return ""
}

// ParseUUIDParam parses a UUID path parameter, writing a 400 on failure.
func ParseUUIDParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	raw := PathParam(r, name)
	if raw == "" {
		// Try extracting from last path segment as fallback
		parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
		if len(parts) > 0 {
			raw = parts[len(parts)-1]
		}
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		WriteError(w, http.StatusBadRequest, name+" must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}

// DecodeBody decodes a JSON request body into v.
func DecodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	return true
}
