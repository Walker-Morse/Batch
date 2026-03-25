// Package middleware provides HTTP middleware for the Card and Member API.
package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/walker-morse/batch/card_member_api/ports"
)

type contextKey string

const claimsKey contextKey = "jwt_claims"

// Claims is the parsed JWT payload from Cognito.
// The concrete Cognito token shape is:
//
//	{
//	  "sub": "<member-uuid>",            // present for UW member tokens
//	  "custom:tenant_id": "rfu-oregon",
//	  "custom:caller_type": "universal_wallet",
//	  "cognito:groups": ["members"]
//	}
//
// For machine-client tokens (IVR, batch, ops):
//
//	{
//	  "client_id": "<service-client-id>",
//	  "custom:tenant_id": "rfu-oregon",
//	  "custom:caller_type": "ivr"
//	}
type Claims struct {
	TenantID        string
	CallerType      string
	SubjectMemberID *uuid.UUID // non-nil for UW member-authenticated tokens
}

// ClaimsFromContext retrieves parsed claims set by JWTAuth middleware.
func ClaimsFromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsKey).(Claims)
	return c, ok
}

// JWTAuth validates the Bearer token and populates Claims on the request context.
//
// In production: parse and verify the Cognito JWT (RS256, JWKS from Cognito user pool).
// During development / before auth is wired: accepts a synthetic header
// X-Dev-Claims: {"tenant_id":"rfu-oregon","caller_type":"universal_wallet","subject_member_id":"<uuid>"}
// This lets the mock integration run without a real Cognito pool.
func JWTAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ── Dev bypass: X-Dev-Claims header ──────────────────────────────────
		if devHeader := r.Header.Get("X-Dev-Claims"); devHeader != "" {
			var raw struct {
				TenantID        string  `json:"tenant_id"`
				CallerType      string  `json:"caller_type"`
				SubjectMemberID *string `json:"subject_member_id"`
			}
			if err := json.Unmarshal([]byte(devHeader), &raw); err == nil {
				claims := Claims{TenantID: raw.TenantID, CallerType: raw.CallerType}
				if raw.SubjectMemberID != nil {
					if id, err := uuid.Parse(*raw.SubjectMemberID); err == nil {
						claims.SubjectMemberID = &id
					}
				}
				ctx := context.WithValue(r.Context(), claimsKey, claims)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		// ── Production: Bearer JWT verification ───────────────────────────────
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
			return
		}
		// TODO: implement Cognito JWT verification (RS256, JWKS endpoint):
		//   1. Fetch JWKS from https://cognito-idp.{region}.amazonaws.com/{pool_id}/.well-known/jwks.json
		//   2. Verify token signature, expiry, issuer, audience
		//   3. Extract claims
		// Blocked on: auth pool setup with John Stevens / UW team
		_ = strings.TrimPrefix(authHeader, "Bearer ")
		http.Error(w, `{"error":"JWT verification not yet implemented — use X-Dev-Claims in dev"}`, http.StatusUnauthorized)
	})
}

// RequireCaller rejects requests from callers not in the allowed set.
// Applied per-route to enforce caller-type authorization.
func RequireCaller(allowed ...ports.CallerType) func(http.Handler) http.Handler {
	set := make(map[ports.CallerType]bool, len(allowed))
	for _, c := range allowed {
		set[c] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFromContext(r.Context())
			if !ok || !set[ports.CallerType(claims.CallerType)] {
				http.Error(w, `{"error":"forbidden — caller not authorized for this operation"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CorrelationID ensures every request has an X-Correlation-ID header and
// adds it to the response. Generates one if missing.
func CorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corrID := r.Header.Get("X-Correlation-ID")
		if corrID == "" {
			corrID = uuid.New().String()
		}
		w.Header().Set("X-Correlation-ID", corrID)
		next.ServeHTTP(w, r)
	})
}

// JSONContentType sets Content-Type: application/json on all responses.
func JSONContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}
