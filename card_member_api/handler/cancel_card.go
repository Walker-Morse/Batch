// Package handler contains the HTTP handlers for the Card and Member API.
//
// Handlers are deliberately thin:
//   - Parse and validate the HTTP request
//   - Populate RequestContext from JWT claims (injected by middleware)
//   - Call the domain service
//   - Map result or error to HTTP response
//
// No business logic lives here. No FIS concepts appear here.
// All domain logic, idempotency, and FIS sequencing is in the service layer.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/walker-morse/batch/card_member_api/ports"
)

// ─── Cancel Card handler ──────────────────────────────────────────────────────

// CancelCardRequest is the JSON body for POST /cards/{id}/cancel.
//
// The {id} path parameter is the One Fintech card UUID (UW path).
// For IVR callers who present a card token instead of a card UUID, use
// POST /cards/cancel with a body containing card_token instead of hitting
// this endpoint with a path parameter.
type cancelCardHTTPRequest struct {
	// CorrelationID is the caller-supplied idempotency key.
	// Must be a v4 UUID. Required on all mutating requests.
	// For dead letter repair this is the original batch correlation ID.
	CorrelationID string `json:"correlation_id"`

	// CancelReason is required.
	CancelReason string `json:"cancel_reason"`

	// CardToken is populated by IVR callers who identify the card by token.
	// Mutually exclusive with the {id} path parameter.
	CardToken *string `json:"card_token,omitempty"`
}

type cancelCardHTTPResponse struct {
	CardID       string `json:"card_id"`
	MemberID     string `json:"member_id"`
	CancelledAt  string `json:"cancelled_at"`
	PursesClosed int    `json:"purses_closed"`
}

// CancelCardHandler handles POST /cards/{id}/cancel and POST /cards/cancel (IVR token path).
type CancelCardHandler struct {
	svc ports.ICardService
}

// NewCancelCardHandler constructs a CancelCardHandler.
func NewCancelCardHandler(svc ports.ICardService) *CancelCardHandler {
	return &CancelCardHandler{svc: svc}
}

// ServeHTTP handles the request.
// JWT claims are expected to be set on the request context by middleware
// before this handler is called. The callerType claim determines which
// resolution path (MemberID vs CardToken) is used.
func (h *CancelCardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// ── Parse body ────────────────────────────────────────────────────────────

	var body cancelCardHTTPRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// ── Validate correlation ID ───────────────────────────────────────────────

	corrID, err := uuid.Parse(body.CorrelationID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "correlation_id must be a valid v4 UUID")
		return
	}

	// ── Build RequestContext from JWT middleware claims ────────────────────────
	// claimsFromContext is a thin helper that reads the claims the JWT middleware
	// set on the context. Defined in middleware package (not shown here).

	claims, ok := claimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing auth context")
		return
	}

	rctx := ports.RequestContext{
		CorrelationID:   corrID,
		TenantID:        claims.TenantID,
		CallerType:      ports.CallerType(claims.CallerType),
		SubjectMemberID: claims.SubjectMemberID,
	}

	// ── Resolve card identity ─────────────────────────────────────────────────
	//
	// UW path: card UUID is in the path parameter, set by router.
	// IVR path: card_token is in the request body; MemberID is nil.

	req := ports.CancelCardRequest{
		Rctx:         rctx,
		CancelReason: ports.CancelReason(body.CancelReason),
	}

	if body.CardToken != nil {
		// IVR token path
		req.CardToken = body.CardToken
	} else {
		// Path parameter path — card UUID from router
		cardIDStr := cardIDFromPath(r)
		if cardIDStr == "" {
			writeError(w, http.StatusBadRequest, "card_id path parameter required when card_token not provided")
			return
		}
		cardID, err := uuid.Parse(cardIDStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "card_id must be a valid UUID")
			return
		}
		// Resolve memberID from cardID via the subject claim on UW requests,
		// or pass cardID and let the service resolve — for simplicity we
		// pass memberID via the subject claim if caller is UW.
		if claims.SubjectMemberID != nil {
			req.MemberID = claims.SubjectMemberID
		} else {
			// Ops portal or internal caller: memberID must be resolvable from cardID.
			// For now, pass cardID as memberID — service will handle resolution.
			// TODO: add GetMemberByCardID to CardRepository.
			_ = cardID
			writeError(w, http.StatusNotImplemented, "card-to-member resolution for non-UW callers not yet implemented")
			return
		}
	}

	// ── Call service ──────────────────────────────────────────────────────────

	result, err := h.svc.CancelCard(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, ports.ErrMemberNotFound):
			writeError(w, http.StatusNotFound, "member not found")
		case errors.Is(err, ports.ErrCardNotFound):
			writeError(w, http.StatusNotFound, "card not found")
		case errors.Is(err, ports.ErrMemberNotResolved):
			writeError(w, http.StatusConflict, "member FIS identifiers not yet resolved — retry after batch return file processing")
		case errors.Is(err, ports.ErrDuplicateRequest):
			writeError(w, http.StatusConflict, "duplicate request — idempotency key already in progress")
		case errors.Is(err, ports.ErrAlreadyCancelled):
			// Idempotent success — card already in desired state.
			// Fall through to 200 if result is non-nil, else 409.
			if result == nil {
				writeError(w, http.StatusConflict, "card already cancelled")
				return
			}
		default:
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		if result == nil {
			return
		}
	}

	// ── Write response ────────────────────────────────────────────────────────

	writeJSON(w, http.StatusOK, cancelCardHTTPResponse{
		CardID:      result.CardID.String(),
		MemberID:    result.MemberID.String(),
		CancelledAt: result.CancelledAt.Format("2006-01-02T15:04:05Z"),
		PursesClosed: result.PursesClosed,
	})
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

type errorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ─── Stubs for middleware integration (to be implemented) ────────────────────

// JWTClaims is the parsed claim set set on the context by JWT middleware.
// CallerType is derived from the Cognito client_id or a custom claim.
type JWTClaims struct {
	TenantID        string
	CallerType      string
	SubjectMemberID *uuid.UUID // non-nil for UW (member-authenticated) requests
}

// claimsFromContext reads JWT claims set by middleware. Stub — replace with
// real context key lookup when middleware is wired.
func claimsFromContext(ctx interface{ Value(any) any }) (JWTClaims, bool) {
	// TODO: implement with real context key
	return JWTClaims{}, false
}

// cardIDFromPath reads the {id} path parameter. Stub — replace with
// real router path variable extraction (chi, gorilla/mux, etc.).
func cardIDFromPath(r *http.Request) string {
	// TODO: implement with router path variable extraction
	// e.g. chi.URLParam(r, "id")
	return ""
}
