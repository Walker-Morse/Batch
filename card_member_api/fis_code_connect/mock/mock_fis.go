// Package mock provides a fully in-memory implementation of IFisCodeConnectPort.
//
// FisCodeConnectMock is used in:
//   - All unit tests (zero network, deterministic, fast)
//   - Local development (run api-server with -fis-mock flag)
//   - Integration tests against Aurora (real DB, fake FIS)
//
// Design principles:
//   - Thread-safe: all state behind a sync.RWMutex
//   - Deterministic: IDs are auto-incremented integers, not random
//   - Configurable errors: inject failures per-method via ErrorInjector
//   - Stateful: tracks persons, cards, purses, transactions across calls
//   - Person-before-card enforced: IssueCard fails if personId unknown
//
// Usage:
//
//	m := mock.NewFisCodeConnectMock()
//	m.InjectError("CloseCard", fis_code_connect.ErrFisServerError)
//	svc := service.NewCardService(m, repo, ...)
package mock

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/walker-morse/batch/card_member_api/fis_code_connect"
)

// FisCodeConnectMock is the in-memory FIS adapter. All state is in-process.
type FisCodeConnectMock struct {
	mu sync.RWMutex

	nextPersonID int32
	nextCardID   int64

	persons      map[fis_code_connect.FisPersonID]*fis_code_connect.FisPerson
	cards        map[fis_code_connect.FisCardID]*fis_code_connect.FisCard
	purses       map[fis_code_connect.FisCardID][]fis_code_connect.FisPurse
	transactions map[fis_code_connect.FisCardID][]fis_code_connect.FisTransaction
	// tokenIndex maps card-number token → cardId (for TranslateCardNumber)
	tokenIndex map[string]fis_code_connect.FisCardID
	// proxyIndex maps proxy → cardId (for TranslateProxy)
	proxyIndex map[string]fis_code_connect.FisCardID

	// errorInjector maps method name → error to return (nil = no injection)
	errorInjector map[string]error

	// Counters for test assertions
	Calls map[string]int
}

// NewFisCodeConnectMock creates a ready-to-use mock.
func NewFisCodeConnectMock() *FisCodeConnectMock {
	return &FisCodeConnectMock{
		nextPersonID:  1000,
		nextCardID:    900000000000000001,
		persons:       make(map[fis_code_connect.FisPersonID]*fis_code_connect.FisPerson),
		cards:         make(map[fis_code_connect.FisCardID]*fis_code_connect.FisCard),
		purses:        make(map[fis_code_connect.FisCardID][]fis_code_connect.FisPurse),
		transactions:  make(map[fis_code_connect.FisCardID][]fis_code_connect.FisTransaction),
		tokenIndex:    make(map[string]fis_code_connect.FisCardID),
		proxyIndex:    make(map[string]fis_code_connect.FisCardID),
		errorInjector: make(map[string]error),
		Calls:         make(map[string]int),
	}
}

// Compile-time assertion.
var _ fis_code_connect.IFisCodeConnectPort = (*FisCodeConnectMock)(nil)

// InjectError causes the named method to return err on the next call.
// Pass nil to clear a previously injected error.
func (m *FisCodeConnectMock) InjectError(method string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		delete(m.errorInjector, method)
	} else {
		m.errorInjector[method] = err
	}
}

// SeedCard directly inserts a card and its purses into the mock — used in tests
// to set up pre-existing state without going through the full enroll flow.
func (m *FisCodeConnectMock) SeedCard(card fis_code_connect.FisCard, purses []fis_code_connect.FisPurse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := card
	m.cards[card.CardID] = &c
	m.purses[card.CardID] = purses
	proxy := fmt.Sprintf("PROXY-%s", card.CardID[len(card.CardID)-6:])
	m.proxyIndex[proxy] = card.CardID
	m.tokenIndex[string(card.CardID)] = card.CardID // token == cardId in mock
}

// Reset clears all state. Use between tests.
func (m *FisCodeConnectMock) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.persons = make(map[fis_code_connect.FisPersonID]*fis_code_connect.FisPerson)
	m.cards = make(map[fis_code_connect.FisCardID]*fis_code_connect.FisCard)
	m.purses = make(map[fis_code_connect.FisCardID][]fis_code_connect.FisPurse)
	m.transactions = make(map[fis_code_connect.FisCardID][]fis_code_connect.FisTransaction)
	m.tokenIndex = make(map[string]fis_code_connect.FisCardID)
	m.proxyIndex = make(map[string]fis_code_connect.FisCardID)
	m.errorInjector = make(map[string]error)
	m.Calls = make(map[string]int)
}

func (m *FisCodeConnectMock) record(method string) error {
	m.Calls[method]++
	if err, ok := m.errorInjector[method]; ok {
		return err
	}
	return nil
}

// ── Person ────────────────────────────────────────────────────────────────────

func (m *FisCodeConnectMock) CreatePerson(_ context.Context, req fis_code_connect.CreatePersonRequest) (fis_code_connect.FisPersonID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.record("CreatePerson"); err != nil {
		return 0, err
	}
	id := fis_code_connect.FisPersonID(m.nextPersonID)
	m.nextPersonID++
	m.persons[id] = &fis_code_connect.FisPerson{
		PersonID:   id,
		ClientID:   req.ClientID,
		FirstName:  req.FirstName,
		LastName:   req.LastName,
		RiskStatus: "Pass",
		InsertedAt: time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	return id, nil
}

func (m *FisCodeConnectMock) GetPerson(_ context.Context, id fis_code_connect.FisPersonID) (*fis_code_connect.FisPerson, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := m.record("GetPerson"); err != nil {
		return nil, err
	}
	p, ok := m.persons[id]
	if !ok {
		return nil, fis_code_connect.ErrFisNotFound
	}
	cp := *p
	return &cp, nil
}

func (m *FisCodeConnectMock) UpdatePerson(_ context.Context, id fis_code_connect.FisPersonID, req fis_code_connect.UpdatePersonRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.record("UpdatePerson"); err != nil {
		return err
	}
	p, ok := m.persons[id]
	if !ok {
		return fis_code_connect.ErrFisNotFound
	}
	if req.FirstName != "" {
		p.FirstName = req.FirstName
	}
	if req.LastName != "" {
		p.LastName = req.LastName
	}
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// ── Card ──────────────────────────────────────────────────────────────────────

func (m *FisCodeConnectMock) IssueCard(_ context.Context, personID fis_code_connect.FisPersonID, req fis_code_connect.IssueCardRequest) (fis_code_connect.FisCardID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.record("IssueCard"); err != nil {
		return "", err
	}
	if _, ok := m.persons[personID]; !ok {
		return "", fmt.Errorf("IssueCard: personID %d not found — CreatePerson must precede IssueCard", personID)
	}
	id := fis_code_connect.FisCardID(fmt.Sprintf("%018d", m.nextCardID))
	m.nextCardID++
	now := time.Now().UTC()
	m.cards[id] = &fis_code_connect.FisCard{
		CardID:             id,
		PersonID:           personID,
		Status:             fis_code_connect.FisCardReady,
		SubprogramID:       req.SubprogramID,
		PackageID:          req.PackageID,
		Last4CardNumber:    string(id)[14:],
		Proxy:              fmt.Sprintf("PROXY-%s", string(id)[12:]),
		PhysicalExpiration: "12/28",
		IsExpired:          false,
		InsertedAt:         now,
		UpdatedAt:          now,
	}
	// Seed two purses for RFU (OTC + FOD)
	m.purses[id] = []fis_code_connect.FisPurse{
		{CardID: id, PurseNumber: 1, PurseName: "OTC2550", Status: fis_code_connect.PurseStatusActive, CurrencyAlphaCode: "USD", EffectiveDate: now, ExpirationDate: now.AddDate(0, 1, 0)},
		{CardID: id, PurseNumber: 2, PurseName: "FOD2550", Status: fis_code_connect.PurseStatusActive, CurrencyAlphaCode: "USD", EffectiveDate: now, ExpirationDate: now.AddDate(0, 1, 0)},
	}
	m.tokenIndex[string(id)] = id
	m.proxyIndex[fmt.Sprintf("PROXY-%s", string(id)[12:])] = id
	return id, nil
}

func (m *FisCodeConnectMock) GetCard(_ context.Context, id fis_code_connect.FisCardID) (*fis_code_connect.FisCard, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := m.record("GetCard"); err != nil {
		return nil, err
	}
	c, ok := m.cards[id]
	if !ok {
		return nil, fis_code_connect.ErrFisNotFound
	}
	cp := *c
	return &cp, nil
}

func (m *FisCodeConnectMock) ActivateCard(_ context.Context, id fis_code_connect.FisCardID, _ fis_code_connect.ActivateCardRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.record("ActivateCard"); err != nil {
		return err
	}
	c, ok := m.cards[id]
	if !ok {
		return fis_code_connect.ErrFisNotFound
	}
	if c.Status == fis_code_connect.FisCardActive {
		return nil // idempotent
	}
	if fis_code_connect.CardIsTerminal(c.Status) {
		return fis_code_connect.ErrFisCardTerminal
	}
	c.Status = fis_code_connect.FisCardActive
	c.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *FisCodeConnectMock) ChangeCardStatus(_ context.Context, id fis_code_connect.FisCardID, req fis_code_connect.ChangeCardStatusRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.record("ChangeCardStatus"); err != nil {
		return err
	}
	c, ok := m.cards[id]
	if !ok {
		return fis_code_connect.ErrFisNotFound
	}
	if fis_code_connect.CardIsTerminal(c.Status) {
		return fis_code_connect.ErrFisCardTerminal
	}
	c.Status = fis_code_connect.FisCardStatus(req.Status)
	c.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *FisCodeConnectMock) CloseCard(_ context.Context, id fis_code_connect.FisCardID, _ fis_code_connect.CloseCardRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.record("CloseCard"); err != nil {
		return err
	}
	c, ok := m.cards[id]
	if !ok {
		return fis_code_connect.ErrFisNotFound
	}
	c.Status = fis_code_connect.FisCardClosed
	c.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *FisCodeConnectMock) LoadFunds(_ context.Context, id fis_code_connect.FisCardID, req fis_code_connect.LoadFundsRequest) (*fis_code_connect.LoadFundsResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.record("LoadFunds"); err != nil {
		return nil, err
	}
	purses, ok := m.purses[id]
	if !ok {
		return nil, fis_code_connect.ErrFisNotFound
	}
	var running int64
	for i, p := range purses {
		if p.PurseNumber == req.PurseNumber {
			purses[i].AvailableBalanceCents += req.AmountCents
			running = purses[i].AvailableBalanceCents
			break
		}
	}
	txID := fis_code_connect.FisTransactionID(fmt.Sprintf("TXN-%s-%d", id, time.Now().UnixNano()))
	m.transactions[id] = append(m.transactions[id], fis_code_connect.FisTransaction{
		TransactionID: txID,
		CardID:        id,
		AmountCents:   req.AmountCents,
		InsertedAt:    time.Now().UTC(),
		Status:        "Settled",
	})
	return &fis_code_connect.LoadFundsResult{
		TransactionID:       txID,
		RunningBalanceCents: running,
	}, nil
}

func (m *FisCodeConnectMock) ReissueCard(_ context.Context, id fis_code_connect.FisCardID, _ fis_code_connect.ReissueCardRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.record("ReissueCard"); err != nil {
		return err
	}
	if _, ok := m.cards[id]; !ok {
		return fis_code_connect.ErrFisNotFound
	}
	return nil
}

func (m *FisCodeConnectMock) ReplaceCard(_ context.Context, id fis_code_connect.FisCardID, _ fis_code_connect.ReplaceCardRequest) (fis_code_connect.FisCardID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.record("ReplaceCard"); err != nil {
		return "", err
	}
	old, ok := m.cards[id]
	if !ok {
		return "", fis_code_connect.ErrFisNotFound
	}
	// Mark old card as Replaced
	old.Status = fis_code_connect.FisCardReplaced
	old.UpdatedAt = time.Now().UTC()
	// Create new card
	newID := fis_code_connect.FisCardID(fmt.Sprintf("%018d", m.nextCardID))
	m.nextCardID++
	now := time.Now().UTC()
	m.cards[newID] = &fis_code_connect.FisCard{
		CardID:             newID,
		PersonID:           old.PersonID,
		Status:             fis_code_connect.FisCardReady,
		SubprogramID:       old.SubprogramID,
		PackageID:          old.PackageID,
		Last4CardNumber:    string(newID)[14:],
		Proxy:              fmt.Sprintf("PROXY-%s", string(newID)[12:]),
		PhysicalExpiration: "12/30",
		InsertedAt:         now,
		UpdatedAt:          now,
	}
	// Transfer purse balances
	if purses, ok := m.purses[id]; ok {
		newPurses := make([]fis_code_connect.FisPurse, len(purses))
		copy(newPurses, purses)
		for i := range newPurses {
			newPurses[i].CardID = newID
		}
		m.purses[newID] = newPurses
	}
	m.tokenIndex[string(newID)] = newID
	return newID, nil
}

func (m *FisCodeConnectMock) RegisterPersonToCard(_ context.Context, cardID fis_code_connect.FisCardID, personID fis_code_connect.FisPersonID, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.record("RegisterPersonToCard"); err != nil {
		return err
	}
	c, ok := m.cards[cardID]
	if !ok {
		return fis_code_connect.ErrFisNotFound
	}
	c.PersonID = personID
	return nil
}

// ── Account / Purse ───────────────────────────────────────────────────────────

func (m *FisCodeConnectMock) GetPurses(_ context.Context, cardID fis_code_connect.FisCardID) ([]fis_code_connect.FisPurse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := m.record("GetPurses"); err != nil {
		return nil, err
	}
	purses, ok := m.purses[cardID]
	if !ok {
		return nil, fis_code_connect.ErrFisNotFound
	}
	cp := make([]fis_code_connect.FisPurse, len(purses))
	copy(cp, purses)
	return cp, nil
}

func (m *FisCodeConnectMock) GetPurse(_ context.Context, cardID fis_code_connect.FisCardID, purseNumber fis_code_connect.FisPurseNumber) (*fis_code_connect.FisPurse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := m.record("GetPurse"); err != nil {
		return nil, err
	}
	for _, p := range m.purses[cardID] {
		if p.PurseNumber == purseNumber {
			cp := p
			return &cp, nil
		}
	}
	return nil, fis_code_connect.ErrFisNotFound
}

func (m *FisCodeConnectMock) SetPurseStatus(_ context.Context, cardID fis_code_connect.FisCardID, purseNumber fis_code_connect.FisPurseNumber, status fis_code_connect.PurseStatusCode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.record("SetPurseStatus"); err != nil {
		return err
	}
	purses, ok := m.purses[cardID]
	if !ok {
		return fis_code_connect.ErrFisNotFound
	}
	for i, p := range purses {
		if p.PurseNumber == purseNumber {
			purses[i].Status = status
			return nil
		}
	}
	return fis_code_connect.ErrFisNotFound
}

// ── Transactions ──────────────────────────────────────────────────────────────

func (m *FisCodeConnectMock) GetCardTransactions(_ context.Context, cardID fis_code_connect.FisCardID, filter fis_code_connect.TransactionFilter) ([]fis_code_connect.FisTransaction, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := m.record("GetCardTransactions"); err != nil {
		return nil, err
	}
	if !filter.StartDate.IsZero() && !filter.EndDate.IsZero() {
		if filter.EndDate.Sub(filter.StartDate) > 30*24*time.Hour {
			return nil, fis_code_connect.ErrTransactionWindowExceeded
		}
	}
	txns := m.transactions[cardID]
	cp := make([]fis_code_connect.FisTransaction, len(txns))
	copy(cp, txns)
	return cp, nil
}

func (m *FisCodeConnectMock) GetTransaction(_ context.Context, id fis_code_connect.FisTransactionID) (*fis_code_connect.FisTransaction, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := m.record("GetTransaction"); err != nil {
		return nil, err
	}
	for _, txns := range m.transactions {
		for _, t := range txns {
			if t.TransactionID == id {
				cp := t
				return &cp, nil
			}
		}
	}
	return nil, fis_code_connect.ErrFisNotFound
}

// ── Translate ─────────────────────────────────────────────────────────────────

func (m *FisCodeConnectMock) TranslateCardNumber(_ context.Context, cardNumber string) (fis_code_connect.FisCardID, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := m.record("TranslateCardNumber"); err != nil {
		return "", err
	}
	id, ok := m.tokenIndex[cardNumber]
	if !ok {
		return "", fis_code_connect.ErrFisNotFound
	}
	return id, nil
}

func (m *FisCodeConnectMock) TranslateProxy(_ context.Context, proxy string, _ int32) (fis_code_connect.FisCardID, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := m.record("TranslateProxy"); err != nil {
		return "", err
	}
	id, ok := m.proxyIndex[proxy]
	if !ok {
		return "", fis_code_connect.ErrFisNotFound
	}
	return id, nil
}
