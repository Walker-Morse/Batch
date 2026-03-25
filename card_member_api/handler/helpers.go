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

type errorResponse struct {
	Error string `json:"error"`
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, errorResponse{Error: msg})
}

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

// BuildRequestContext extracts JWT claims and both ID headers into a RequestContext.
//
// X-Correlation-ID — per-request trace ID. Auto-generated if absent. Used in logs only.
// X-Idempotency-Key — caller-supplied operation dedup key. Required on mutating requests.
//                     Stable across retries. Feeds the domain_commands idempotency gate.
func BuildRequestContext(r *http.Request) (ports.RequestContext, bool) {
	claims, ok := middleware.ClaimsFromContext(r.Context())
	if !ok {
		return ports.RequestContext{}, false
	}

	// Correlation ID — trace only, generate if missing
	corrIDStr := r.Header.Get("X-Correlation-ID")
	corrID, err := uuid.Parse(corrIDStr)
	if err != nil {
		corrID = uuid.New()
	}

	// Idempotency key — caller must supply on mutating operations
	idemKeyStr := r.Header.Get("X-Idempotency-Key")
	idemKey, err := uuid.Parse(idemKeyStr)
	if err != nil {
		// Not present or invalid — generate one. Service layer will accept it but
		// callers who don't supply this cannot benefit from idempotent retry behaviour.
		idemKey = uuid.New()
	}

	return ports.RequestContext{
		CorrelationID:   corrID,
		IdempotencyKey:  idemKey,
		TenantID:        claims.TenantID,
		CallerType:      ports.CallerType(claims.CallerType),
		SubjectMemberID: claims.SubjectMemberID,
	}, true
}

func PathParam(r *http.Request, name string) string {
	if v := r.PathValue(name); v != "" {
		return v
	}
	return ""
}

func ParseUUIDParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	raw := PathParam(r, name)
	if raw == "" {
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

func DecodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	return true
}
