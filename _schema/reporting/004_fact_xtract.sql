-- One Fintech Platform — XTRACT-Dependent Fact Tables
-- §4.3.8 fact_card_loads, §4.3.9 fact_purchases
--
-- STATUS: DEFERRED — blocked on DM-03 (FIS Data XTRACT contract).
-- Owner: Kendra Williams (confirm Morse has contracted XTRACT feeds).
-- These tables are created with their full schema now so the reporting
-- schema is structurally complete and the Excel report generator can
-- reference them. They will be empty until DM-03 is resolved and the
-- XTRACT ETL writer is implemented.
--
-- DO NOT populate these tables from any source other than the FIS XTRACT
-- feeds. The operational tables (purses.available_balance_cents) are the
-- One Fintech ledger view; these fact tables are the FIS source of record.
-- They must reconcile but must not be conflated.
--
-- Source feeds:
--   fact_card_loads   — FIS Monetary XTRACT v3.8   (MON)
--   fact_purchases    — FIS Authorization XTRACT v2.7 (AUTH)
--                     + FIS Filtered Spend Item Details XTRACT v1.3 (FSID)

-- ─── fact_card_loads ─────────────────────────────────────────────────────────
-- Source: FIS Monetary XTRACT v3.8. One row per value load transaction.
-- TxnTypeCode (field 19) = 1 (ValueLoad) identifies loads.
-- Key XTRACT field mappings per §4.3.8:
--   SubProgramID (field 4)     → dim_program
--   PAN proxy (field 44)       → dim_card
--   Purse No (field 60)        → dim_purse
--   Txn Local Amount (field 14, 4 decimal places) × 100 → amount_cents
--   WCSUTCPostDate (field 34)  → date_sk
-- Amounts stored as integer cents (4-decimal field × 100, truncate).
CREATE TABLE IF NOT EXISTS reporting.fact_card_loads (
    fact_card_load_sk       BIGSERIAL       PRIMARY KEY,
    -- Dimension foreign keys
    dim_card_sk             BIGINT          NOT NULL
                            REFERENCES reporting.dim_card(card_sk),
    dim_purse_sk            BIGINT
                            REFERENCES reporting.dim_purse(purse_sk),
    dim_member_sk           BIGINT          NOT NULL
                            REFERENCES reporting.dim_member(member_sk),
    dim_program_sk          BIGINT
                            REFERENCES reporting.dim_program(dim_program_sk),
    date_sk                 INTEGER
                            REFERENCES reporting.dim_date(date_sk),        -- WCSUTCPostDate
    -- Degenerate dimensions
    tenant_id               TEXT            NOT NULL,
    -- FIS XTRACT fields
    fis_txn_uid             VARCHAR(36),    -- MON field 12: TxnUID
    txn_type_code           SMALLINT
                            REFERENCES reporting.dim_txn_type(txn_type_code),
                            -- MON field 19: 1=ValueLoad, 7=Adjustment
    load_type               TEXT
                            CHECK (load_type IN ('INITIAL','RELOAD','ADJUSTMENT')),
                            -- derived from initial_load_date flag in MON feed
    amount_cents            BIGINT          NOT NULL,
                            -- MON field 14 (Txn Local Amount) × 100; integer cents
    settle_amount_cents     BIGINT,         -- MON field 33 (Settle Amount) × 100
    txn_sign                SMALLINT
                            CHECK (txn_sign IN (1, -1)),
                            -- MON field 16: 1=credit, -1=debit
    xtract_file_date        DATE            NOT NULL,   -- MON FileDate field 4
    created_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    -- Idempotency key (MON TxnUID + purse combo)
    UNIQUE (fis_txn_uid, dim_purse_sk)
);

CREATE INDEX IF NOT EXISTS fact_loads_member_date
    ON reporting.fact_card_loads (dim_member_sk, date_sk);
CREATE INDEX IF NOT EXISTS fact_loads_purse_date
    ON reporting.fact_card_loads (dim_purse_sk, date_sk);
CREATE INDEX IF NOT EXISTS fact_loads_program_date
    ON reporting.fact_card_loads (dim_program_sk, date_sk);

COMMENT ON TABLE reporting.fact_card_loads IS
'Value load fact table. §4.3.8. DEFERRED — blocked on DM-03 (Kendra Williams).
Source: FIS Monetary XTRACT v3.8 (MON). Empty until XTRACT ETL is implemented.
Key field mappings in §4.3.8. Amounts in integer cents (MON field 14 × 100).
Primary source for benefit utilization report (§4.4.4).';

-- ─── fact_purchases ──────────────────────────────────────────────────────────
-- Source: FIS Authorization XTRACT v2.7 (AUTH) + Filtered Spend Item
-- Details XTRACT v1.3 (FSID). One row per authorization event.
-- Key XTRACT field mappings per §4.3.9:
--   TxnUID (AUTH field 12)           — natural key
--   SubprogramID (AUTH field 4)      → dim_program
--   PAN Proxy Number (AUTH field 62) → dim_card
--   Purse Number (AUTH field 15)     → dim_purse
--   Authorization Amount (AUTH field 27) → auth_amount_cents
--   MCC (AUTH field 40)              — APL/Tableau filtering
--   Merchant Name (AUTH field 44), Merchant Number (AUTH field 45)
--   iias_validated                   — FSID field 64: IsOnApl flag (0/1)
--     NOTE: FSID v1.3 has 74 fields; field 90 referenced in prior drafts
--     does not exist. Field 64 confirmed from sample data; verify against
--     FSID v1.3 spec before ETL build.
--   apl_filter_response              — FSID field 40: APL purse-split detail
--     e.g. "PRS APRV: 5D - 25.20" or "PRS APRV: 5A - 14.79 & 5D - 6.67"
--     Primary RFU OTC/food analytics signal. NOT a spend category label.
CREATE TABLE IF NOT EXISTS reporting.fact_purchases (
    fact_purchase_sk        BIGSERIAL       PRIMARY KEY,
    -- Dimension foreign keys
    dim_card_sk             BIGINT          NOT NULL
                            REFERENCES reporting.dim_card(card_sk),
    dim_purse_sk            BIGINT
                            REFERENCES reporting.dim_purse(purse_sk),
    dim_member_sk           BIGINT          NOT NULL
                            REFERENCES reporting.dim_member(member_sk),
    dim_program_sk          BIGINT
                            REFERENCES reporting.dim_program(dim_program_sk),
    date_sk                 INTEGER
                            REFERENCES reporting.dim_date(date_sk),
    -- Degenerate dimensions
    tenant_id               TEXT            NOT NULL,
    -- FIS AUTH XTRACT fields
    fis_txn_uid             VARCHAR(36),    -- AUTH field 12: TxnUID (natural key)
    txn_type_code           SMALLINT
                            REFERENCES reporting.dim_txn_type(txn_type_code),
    auth_amount_cents       BIGINT          NOT NULL,
                            -- AUTH field 27 × 100; integer cents
    available_balance_cents BIGINT,         -- AUTH field 57: balance after auth
                            -- NOTE: fields 57/58 are purse balance snapshots,
                            -- not settlement amounts. Settlement sourced from MON field 33.
    response_code           NUMERIC(10),    -- AUTH field 21: 0=Approved
    is_reversed             BOOLEAN,        -- AUTH field 33
    -- Merchant data
    mcc                     NUMERIC(10),    -- AUTH field 40: MCC
    merchant_name           VARCHAR(40),    -- AUTH field 44
    merchant_number         VARCHAR(16),    -- AUTH field 45
    merchant_state          CHAR(2),        -- AUTH field 49
    -- FSID XTRACT fields (item-level APL detail)
    iias_validated          BOOLEAN,
                            -- FSID field 64: IsOnApl flag (0/1)
                            -- FSID v1.3 has 74 fields; field 90 does not exist.
                            -- Verify field 64 against FSID v1.3 spec before ETL build.
    apl_filter_response     TEXT,
                            -- FSID field 40: APL purse-split auth detail
                            -- e.g. "PRS APRV: 5D - 25.20"
                            -- Primary RFU OTC/food analytics signal.
    xtract_file_date        DATE            NOT NULL,
    created_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    -- Idempotency key (AUTH TxnUID is globally unique)
    UNIQUE (fis_txn_uid)
);

CREATE INDEX IF NOT EXISTS fact_purch_member_date
    ON reporting.fact_purchases (dim_member_sk, date_sk);
CREATE INDEX IF NOT EXISTS fact_purch_purse_date
    ON reporting.fact_purchases (dim_purse_sk, date_sk);
CREATE INDEX IF NOT EXISTS fact_purch_mcc
    ON reporting.fact_purchases (mcc)
    WHERE mcc IS NOT NULL;
CREATE INDEX IF NOT EXISTS fact_purch_response
    ON reporting.fact_purchases (response_code, date_sk);
CREATE INDEX IF NOT EXISTS fact_purch_iias
    ON reporting.fact_purchases (iias_validated)
    WHERE iias_validated IS NOT NULL;

COMMENT ON TABLE reporting.fact_purchases IS
'Authorization event fact table. §4.3.9. DEFERRED — blocked on DM-03 (Kendra Williams).
Source: FIS AUTH XTRACT v2.7 + FSID XTRACT v1.3. Empty until XTRACT ETL implemented.
FSID v1.3 has 74 fields — field 90 does not exist; iias_validated = field 64.
Verify all field mappings against FSID v1.3 spec before ETL build.
apl_filter_response (FSID field 40) is the primary RFU OTC/food analytics signal.';
