// Package handler contains the HTTP handlers for the Card and Member API.
// Handlers are intentionally thin: parse → build RequestContext → call service → map response.
// Zero business logic lives here.
package handler

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/walker-morse/batch/card_member_api/ports"
)

// ════════════════════════════════════════════════════════════════════════════════
// MEMBER HANDLERS
// ════════════════════════════════════════════════════════════════════════════════

// EnrollMemberHandler handles POST /members
type EnrollMemberHandler struct{ svc ports.IMemberService }

func NewEnrollMemberHandler(svc ports.IMemberService) *EnrollMemberHandler {
	return &EnrollMemberHandler{svc: svc}
}

func (h *EnrollMemberHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rctx, ok := BuildRequestContext(r)
	if !ok {
		WriteError(w, http.StatusUnauthorized, "missing auth context")
		return
	}
	var body struct {
		ClientMemberID string `json:"client_member_id"`
		SubprogramID   int64  `json:"subprogram_id"`
		BenefitPeriod  string `json:"benefit_period"`
		FirstName      string `json:"first_name"`
		LastName       string `json:"last_name"`
		DOB            string `json:"dob"` // "2006-01-02"
		Address1       string `json:"address1"`
		Address2       string `json:"address2"`
		City           string `json:"city"`
		State          string `json:"state"`
		ZIP            string `json:"zip"`
		Phone          string `json:"phone"`
		Email          string `json:"email"`
		CardDesignID   string `json:"card_design_id"`
	}
	if !DecodeBody(w, r, &body) {
		return
	}
	dob, _ := time.Parse("2006-01-02", body.DOB)
	result, err := h.svc.EnrollMember(r.Context(), ports.EnrollMemberRequest{
		Rctx: rctx, ClientMemberID: body.ClientMemberID,
		SubprogramID: body.SubprogramID, BenefitPeriod: body.BenefitPeriod,
		FirstName: body.FirstName, LastName: body.LastName, DOB: dob,
		Address1: body.Address1, Address2: body.Address2, City: body.City,
		State: body.State, ZIP: body.ZIP, Phone: body.Phone, Email: body.Email,
		CardDesignID: body.CardDesignID,
	})
	if err != nil {
		MapServiceError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{
		"member_id":    result.MemberID,
		"card_id":      result.CardID,
		"fis_resolved": result.FISResolved,
		"created_at":   result.CreatedAt.Format(time.RFC3339),
	})
}

// ─────────────────────────────────────────────────────────────────────────────

// GetMemberHandler handles GET /members/{id}
type GetMemberHandler struct{ svc ports.IMemberService }

func NewGetMemberHandler(svc ports.IMemberService) *GetMemberHandler {
	return &GetMemberHandler{svc: svc}
}

func (h *GetMemberHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rctx, ok := BuildRequestContext(r)
	if !ok {
		WriteError(w, http.StatusUnauthorized, "missing auth context")
		return
	}
	memberID, ok := ParseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	view, err := h.svc.GetMember(r.Context(), rctx, memberID)
	if err != nil {
		MapServiceError(w, err)
		return
	}
	resp := map[string]any{
		"member_id":       view.MemberID,
		"client_member_id": view.ClientMemberID,
		"status":          view.Status,
		"fis_resolved":    view.FISResolved,
		"created_at":      view.CreatedAt.Format(time.RFC3339),
		"updated_at":      view.UpdatedAt.Format(time.RFC3339),
	}
	if view.CardID != nil {
		resp["card_id"] = view.CardID
	}
	if view.CardStatus != nil {
		resp["card_status"] = view.CardStatus
	}
	WriteJSON(w, http.StatusOK, resp)
}

// ─────────────────────────────────────────────────────────────────────────────

// UpdateMemberHandler handles PUT /members/{id}
type UpdateMemberHandler struct{ svc ports.IMemberService }

func NewUpdateMemberHandler(svc ports.IMemberService) *UpdateMemberHandler {
	return &UpdateMemberHandler{svc: svc}
}

func (h *UpdateMemberHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rctx, ok := BuildRequestContext(r)
	if !ok {
		WriteError(w, http.StatusUnauthorized, "missing auth context")
		return
	}
	memberID, ok := ParseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		DOB       string `json:"dob"`
		Address1  string `json:"address1"`
		Address2  string `json:"address2"`
		City      string `json:"city"`
		State     string `json:"state"`
		ZIP       string `json:"zip"`
		Phone     string `json:"phone"`
		Email     string `json:"email"`
	}
	if !DecodeBody(w, r, &body) {
		return
	}
	dob, _ := time.Parse("2006-01-02", body.DOB)
	req := ports.UpdateMemberRequest{
		Rctx: rctx, MemberID: memberID,
		FirstName: body.FirstName, LastName: body.LastName, DOB: dob,
		Address1: body.Address1, Address2: body.Address2, City: body.City,
		State: body.State, ZIP: body.ZIP, Phone: body.Phone, Email: body.Email,
	}
	if err := h.svc.UpdateMember(r.Context(), req); err != nil {
		MapServiceError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ════════════════════════════════════════════════════════════════════════════════
// CARD HANDLERS
// ════════════════════════════════════════════════════════════════════════════════

// CancelCardHandler handles POST /cards/{id}/cancel and POST /cards/cancel (IVR token path)
type CancelCardHandler struct{ svc ports.ICardService }

func NewCancelCardHandler(svc ports.ICardService) *CancelCardHandler {
	return &CancelCardHandler{svc: svc}
}

func (h *CancelCardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rctx, ok := BuildRequestContext(r)
	if !ok {
		WriteError(w, http.StatusUnauthorized, "missing auth context")
		return
	}
	var body struct {
		CancelReason string  `json:"cancel_reason"`
		CardToken    *string `json:"card_token"`
	}
	if !DecodeBody(w, r, &body) {
		return
	}
	req := ports.CancelCardRequest{Rctx: rctx, CancelReason: ports.CancelReason(body.CancelReason)}
	if body.CardToken != nil {
		req.CardToken = body.CardToken
	} else {
		// memberID from JWT subject claim (UW) or path param
		if rctx.SubjectMemberID != nil {
			req.MemberID = rctx.SubjectMemberID
		} else {
			memberID, ok := ParseUUIDParam(w, r, "id")
			if !ok {
				return
			}
			req.MemberID = &memberID
		}
	}
	result, err := h.svc.CancelCard(r.Context(), req)
	if err != nil {
		MapServiceError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"card_id":       result.CardID,
		"member_id":     result.MemberID,
		"cancelled_at":  result.CancelledAt.Format(time.RFC3339),
		"purses_closed": result.PursesClosed,
	})
}

// ─────────────────────────────────────────────────────────────────────────────

// ReplaceCardHandler handles POST /cards/{id}/replace
type ReplaceCardHandler struct{ svc ports.ICardService }

func NewReplaceCardHandler(svc ports.ICardService) *ReplaceCardHandler {
	return &ReplaceCardHandler{svc: svc}
}

func (h *ReplaceCardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rctx, ok := BuildRequestContext(r)
	if !ok {
		WriteError(w, http.StatusUnauthorized, "missing auth context")
		return
	}
	memberID, ok := ParseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !DecodeBody(w, r, &body) {
		return
	}
	result, err := h.svc.ReplaceCard(r.Context(), ports.ReplaceCardRequest{
		Rctx: rctx, MemberID: memberID, Reason: ports.ReplaceReason(body.Reason),
	})
	if err != nil {
		MapServiceError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"old_card_id":  result.OldCardID,
		"new_card_id":  result.NewCardID,
		"member_id":    result.MemberID,
		"replaced_at":  result.ReplacedAt.Format(time.RFC3339),
	})
}

// ─────────────────────────────────────────────────────────────────────────────

// LoadFundsHandler handles POST /cards/{id}/load
type LoadFundsHandler struct{ svc ports.ICardService }

func NewLoadFundsHandler(svc ports.ICardService) *LoadFundsHandler {
	return &LoadFundsHandler{svc: svc}
}

func (h *LoadFundsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rctx, ok := BuildRequestContext(r)
	if !ok {
		WriteError(w, http.StatusUnauthorized, "missing auth context")
		return
	}
	memberID, ok := ParseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		BenefitType   string `json:"benefit_type"`
		AmountCents   int64  `json:"amount_cents"`
		BenefitPeriod string `json:"benefit_period"`
	}
	if !DecodeBody(w, r, &body) {
		return
	}
	result, err := h.svc.LoadFunds(r.Context(), ports.LoadFundsRequest{
		Rctx: rctx, MemberID: memberID,
		BenefitType: body.BenefitType, AmountCents: body.AmountCents,
		BenefitPeriod: body.BenefitPeriod,
	})
	if err != nil {
		MapServiceError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"card_id":      result.CardID,
		"purse_id":     result.PurseID,
		"amount_cents": result.AmountCents,
		"loaded_at":    result.LoadedAt.Format(time.RFC3339),
	})
}

// ─────────────────────────────────────────────────────────────────────────────

// GetBalanceHandler handles GET /cards/{id}/balance
type GetBalanceHandler struct{ svc ports.ICardService }

func NewGetBalanceHandler(svc ports.ICardService) *GetBalanceHandler {
	return &GetBalanceHandler{svc: svc}
}

func (h *GetBalanceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rctx, ok := BuildRequestContext(r)
	if !ok {
		WriteError(w, http.StatusUnauthorized, "missing auth context")
		return
	}
	memberID := uuid.Nil
	if rctx.SubjectMemberID != nil {
		memberID = *rctx.SubjectMemberID
	} else {
		var ok bool
		memberID, ok = ParseUUIDParam(w, r, "id")
		if !ok {
			return
		}
	}
	result, err := h.svc.GetBalance(r.Context(), ports.GetBalanceRequest{Rctx: rctx, MemberID: memberID})
	if err != nil {
		MapServiceError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"member_id": result.MemberID,
		"card_id":   result.CardID,
		"purses":    result.Purses,
		"as_of":     result.AsOf.Format(time.RFC3339),
	})
}

// ─────────────────────────────────────────────────────────────────────────────

// ResolveCardHandler handles POST /cards/resolve (IVR token → member)
type ResolveCardHandler struct{ svc ports.ICardService }

func NewResolveCardHandler(svc ports.ICardService) *ResolveCardHandler {
	return &ResolveCardHandler{svc: svc}
}

func (h *ResolveCardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rctx, ok := BuildRequestContext(r)
	if !ok {
		WriteError(w, http.StatusUnauthorized, "missing auth context")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if !DecodeBody(w, r, &body) {
		return
	}
	result, err := h.svc.ResolveCard(r.Context(), ports.ResolveCardRequest{Rctx: rctx, Token: body.Token})
	if err != nil {
		MapServiceError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"member_id": result.MemberID,
		"card_id":   result.CardID,
		"status":    result.Status,
	})
}
