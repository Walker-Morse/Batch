// Package router assembles the HTTP mux for the Card and Member API.
// All routes, middleware ordering, and handler registration live here.
package router

import (
	"net/http"

	"github.com/walker-morse/batch/card_member_api/handler"
	"github.com/walker-morse/batch/card_member_api/handler/middleware"
	"github.com/walker-morse/batch/card_member_api/openapi"
	"github.com/walker-morse/batch/card_member_api/ports"
)

// New builds and returns the fully configured HTTP mux.
//
// Route table:
//
//	POST   /v1/members               EnrollMember
//	GET    /v1/members/{id}          GetMember
//	PUT    /v1/members/{id}          UpdateMember
//	POST   /v1/cards/{id}/cancel     CancelCard
//	POST   /v1/cards/{id}/replace    ReplaceCard
//	POST   /v1/cards/{id}/load       LoadFunds
//	GET    /v1/cards/{id}/balance    GetBalance
//	POST   /v1/cards/resolve         ResolveCard
//	GET    /healthz                  Health check
//	GET    /docs/                    Swagger UI
//	GET    /docs/openapi.yaml        Raw OpenAPI spec
func New(memberSvc ports.IMemberService, cardSvc ports.ICardService) http.Handler {
	mux := http.NewServeMux()

	// ── Global middleware stack ────────────────────────────────────────────────
	// Applied to every request via the chain helper.
	global := chain(
		middleware.CorrelationID,
		middleware.JSONContentType,
		middleware.JWTAuth,
	)

	// ── Member routes ─────────────────────────────────────────────────────────

	// POST /v1/members — all authenticated callers can enroll
	mux.Handle("POST /v1/members", global(
		handler.NewEnrollMemberHandler(memberSvc),
	))

	// GET /v1/members/{id} — UW, IVR, ops
	mux.Handle("GET /v1/members/{id}", global(
		middleware.RequireCaller(
			ports.CallerUniversalWallet,
			ports.CallerIVR,
			ports.CallerOpsPortal,
			ports.CallerBatchPipeline,
			ports.CallerDeadLetterRepair,
		)(handler.NewGetMemberHandler(memberSvc)),
	))

	// PUT /v1/members/{id} — UW and ops only
	mux.Handle("PUT /v1/members/{id}", global(
		middleware.RequireCaller(
			ports.CallerUniversalWallet,
			ports.CallerOpsPortal,
		)(handler.NewUpdateMemberHandler(memberSvc)),
	))

	// ── Card routes ───────────────────────────────────────────────────────────

	// POST /v1/cards/resolve — IVR entry point (before cancel/balance)
	// Must be registered before /v1/cards/{id}/... patterns
	mux.Handle("POST /v1/cards/resolve", global(
		middleware.RequireCaller(
			ports.CallerIVR,
			ports.CallerOpsPortal,
		)(handler.NewResolveCardHandler(cardSvc)),
	))

	// POST /v1/cards/{id}/cancel
	mux.Handle("POST /v1/cards/{id}/cancel", global(
		handler.NewCancelCardHandler(cardSvc),
	))

	// POST /v1/cards/{id}/replace — UW and ops only
	mux.Handle("POST /v1/cards/{id}/replace", global(
		middleware.RequireCaller(
			ports.CallerUniversalWallet,
			ports.CallerOpsPortal,
		)(handler.NewReplaceCardHandler(cardSvc)),
	))

	// POST /v1/cards/{id}/load — repair and ops only (batch uses pipeline)
	mux.Handle("POST /v1/cards/{id}/load", global(
		middleware.RequireCaller(
			ports.CallerDeadLetterRepair,
			ports.CallerOpsPortal,
			ports.CallerBatchPipeline,
		)(handler.NewLoadFundsHandler(cardSvc)),
	))

	// GET /v1/cards/{id}/balance — UW, IVR, ops
	mux.Handle("GET /v1/cards/{id}/balance", global(
		middleware.RequireCaller(
			ports.CallerUniversalWallet,
			ports.CallerIVR,
			ports.CallerOpsPortal,
		)(handler.NewGetBalanceHandler(cardSvc)),
	))

	// ── Infrastructure ────────────────────────────────────────────────────────

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"card-member-api"}`))
	})

	// Swagger UI + spec (no auth required — internal network only in prod)
	mux.Handle("/docs/", openapi.Handler())

	return mux
}

// chain composes middleware in left-to-right order (first = outermost).
func chain(middlewares ...func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(final http.Handler) http.Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			final = middlewares[i](final)
		}
		return final
	}
}
