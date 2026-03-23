// Package srg defines the SRG file record types for SRG310, SRG315, and SRG320.
//
// SRG (Standard Record Group) files are pipe-delimited files sent by health plan clients (MCOs).
// Field definitions are sourced from the confirmed column definitions (Open Item #9,
// target Mar 30, 2026 — current implementation uses best-effort from HLD §4.1.2/4.1.3/4.1.4).
//
// All demographic fields (name, DOB, address) are PHI under HIPAA 45 CFR § 164.312.
// raw_payload in batch_records stores the original row as JSONB for replay.
// Never log demographic field values at any log level (§7.2, OWASP ASVS 7.1.1).
//
// ASCII transliteration for non-ASCII names (Spanish-language members) must be
// confirmed before Stage 3 go-live (§2 Assumptions, Open Item #9).
package srg

import "time"

// SRG310Row is one row from a member enrollment / demographic update file.
// Generated from SRG310 inbound pipe-delimited file. Maps to ENROLL or UPDATE domain commands.
type SRG310Row struct {
	// Sequence position within the file (1-based) — used as row idempotency key component
	SequenceInFile int

	// Member identity
	ClientMemberID string // health plan member ID — PHI; natural key
	SubprogramID   string // FIS SubprogramId e.g. "26071"

	// Demographics — PHI
	FirstName   string
	LastName    string
	DOB         time.Time // date of birth
	Address1    string
	Address2    string
	City        string
	State       string // 2-char
	ZIP         string
	PhoneNumber int64  // PHI; digits only; 0 when not provided
	Email       string

	// Card configuration
	PackageID    string // FIS PackageId — from ACC upload
	CardDesignID string
	CustomCardID string

	// Program info
	ContractPBP   string // Plan Benefit Package
	BenefitType   string // OTC|FOD|CMB
	BenefitPeriod string // ISO YYYY-MM

	// Raw pipe-delimited line — stored as JSONB in batch_records_rt30.raw_payload for replay
	// PHI present — never log this field
	Raw map[string]string
}

// SRG315Row is one row from a member status change / purse lifecycle file.
// Maps to UPDATE, SUSPEND, TERMINATE, or SWEEP domain commands.
type SRG315Row struct {
	SequenceInFile int
	ClientMemberID string
	EventType      string    // ACTIVATE|SUSPEND|TERMINATE|EXPIRE_PURSE|WALLET_TRANSFER
	EffectiveDate  time.Time
	BenefitPeriod  string
	BenefitType    string
	Raw            map[string]string
}

// SRG320Row is one row from a fund management file (recurring loads, cashOut).
// Maps to LOAD or SWEEP domain commands.
type SRG320Row struct {
	SequenceInFile int
	ClientMemberID string
	CommandType    string    // LOAD|CASHOUT
	AmountCents    int64     // load amount in cents; from SRG field (e.g. "95.00" → 9500)
	BenefitPeriod  string
	BenefitType    string
	EffectiveDate  time.Time
	ExpiryDate     time.Time // contractual 11:59 PM ET month-end (SOW §2.1)
	Raw            map[string]string
}
