-- One Fintech Platform — Reporting Dimension Tables
-- §4.3.1 dim_member, §4.3.2 dim_card, §4.3.3 dim_purse,
-- §4.3.5 dim_program, §4.3.11 dim_txn_type
--
-- dim_date is in 002_dim_date_seed.sql (requires data population).
-- Execution order: 000_schema_roles.sql → this file → 002 → 003 → 004.
-- All idempotent via CREATE TABLE IF NOT EXISTS.
-- SCD Type 2 columns (row_effective_date / row_expiry_date / is_current)
-- are on dim_member and dim_card as specified in §4.3.1–4.3.2.

-- ─── dim_member ──────────────────────────────────────────────────────────────
-- Source: SRG310 (initial enrollment), SRG315 (change), SRG320 (reload).
-- Grain: one row per member version (SCD Type 2).
-- Natural key: (client_member_id, tenant_id) on the current row.
-- PHI fields annotated per §5.4.1 (HIPAA 45 CFR § 164.312).
-- DM-01 blocker: purse_code (health-plan namespace) is nullable pending
-- John Stevens confirmation of the FIS-side purse mapping.
CREATE TABLE IF NOT EXISTS reporting.dim_member (
    member_sk               BIGSERIAL       PRIMARY KEY,
    -- Natural / business keys
    tenant_id               TEXT            NOT NULL,
    client_member_id        TEXT            NOT NULL,           -- PHI
    -- Demographics (PHI)
    first_name              VARCHAR(50)     NOT NULL,           -- PHI
    last_name               VARCHAR(50)     NOT NULL,           -- PHI
    date_of_birth           DATE,                               -- PHI
    -- Address
    address1                VARCHAR(80),                        -- PHI
    address2                VARCHAR(80),
    city                    VARCHAR(40),
    state                   CHAR(2),
    zip                     VARCHAR(10),
    county                  VARCHAR(60),    -- Tableau filter for RFU county analysis (§4.4.5.4)
    -- FIS identifiers (NULL until Stage 7 RT30 reconciliation)
    fis_person_id           VARCHAR(20),
    fis_cuid                VARCHAR(19),
    -- Program linkage
    fis_subprogram_id       NUMERIC(10)     NOT NULL,
    -- Processor (extensible for InComm Healthcare peer adapter)
    processor               VARCHAR(20)     NOT NULL DEFAULT 'FIS',
    -- SCD Type 2 versioning
    row_effective_date      TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    row_expiry_date         TIMESTAMPTZ     NOT NULL DEFAULT '9999-12-31',
    is_current              BOOLEAN         NOT NULL DEFAULT TRUE,
    created_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS dim_member_natural_key
    ON reporting.dim_member (client_member_id, tenant_id)
    WHERE is_current;
CREATE INDEX IF NOT EXISTS dim_member_fis_person
    ON reporting.dim_member (fis_person_id)
    WHERE fis_person_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS dim_member_state
    ON reporting.dim_member (state, county)
    WHERE is_current;

COMMENT ON TABLE reporting.dim_member IS
'SCD Type 2 member dimension. PHI — access restricted to tableau_ro.
Natural key: (client_member_id, tenant_id) on current row (is_current=TRUE).
Source: SRG310/315/320 via MartWriter.UpsertMember (§4.3.12).';

-- ─── dim_card ────────────────────────────────────────────────────────────────
-- Source: Stage 7 RT30 return file (card issuance confirmation).
-- Grain: one row per physical card (SCD Type 2 for replacements).
-- fis_card_id is the XTRACT join key — opt-in required (Selvi Marappan, OI #?).
-- Full PAN is never stored; pan_masked carries first 6 + last 4 only.
CREATE TABLE IF NOT EXISTS reporting.dim_card (
    card_sk                 BIGSERIAL       PRIMARY KEY,
    -- Natural / business keys
    tenant_id               TEXT            NOT NULL,
    client_member_id        TEXT            NOT NULL,           -- denormalised for query convenience
    -- FIS identifiers
    fis_card_id             VARCHAR(19),    -- NULL until opt-in confirmed (Selvi Marappan)
    pan_masked              VARCHAR(19),    -- first 6 + last 4; full PAN never stored
    fis_proxy_number        VARCHAR(30),    -- proxy number used in batch submissions
    -- Card state
    card_status             SMALLINT        NOT NULL DEFAULT 1,
                            -- 1=Ready 2=Active 4=Lost 6=Suspended 7=Closed
    card_design_id          TEXT,
    package_id              TEXT,
    -- Dates
    issued_at               TIMESTAMPTZ,   -- set when RT30 return confirms issuance (Stage 7)
    activated_at            TIMESTAMPTZ,
    expired_at              TIMESTAMPTZ,
    closed_at               TIMESTAMPTZ,
    -- FK to member dimension
    dim_member_sk           BIGINT          REFERENCES reporting.dim_member(member_sk),
    -- Processor
    processor               VARCHAR(20)     NOT NULL DEFAULT 'FIS',
    -- SCD Type 2
    row_effective_date      TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    row_expiry_date         TIMESTAMPTZ     NOT NULL DEFAULT '9999-12-31',
    is_current              BOOLEAN         NOT NULL DEFAULT TRUE,
    created_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS dim_card_fis_card_id
    ON reporting.dim_card (fis_card_id)
    WHERE fis_card_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS dim_card_member
    ON reporting.dim_card (dim_member_sk);
CREATE INDEX IF NOT EXISTS dim_card_proxy
    ON reporting.dim_card (fis_proxy_number)
    WHERE fis_proxy_number IS NOT NULL;

COMMENT ON TABLE reporting.dim_card IS
'SCD Type 2 card dimension. fis_card_id is the XTRACT feed join key —
NULL until FIS CardID opt-in is confirmed by Selvi Marappan.
Full PAN never stored; pan_masked = first 6 + last 4 only (§5.4.1).
Source: Stage 7 RT30 return via MartWriter.UpsertCard (§4.3.12).';

-- ─── dim_purse ───────────────────────────────────────────────────────────────
-- Source: ACC configuration (initial), Monetary/Auth/Balance XTRACT (lifecycle).
-- Grain: one row per card-purse-benefit-period.
-- purse_type is GENERATED from first 3 chars of fis_purse_name — the
-- recommended Tableau filter for benefit-type aggregation (§4.4.5.3).
-- purse_code (health-plan namespace) is NULL — DM-01 blocker.
CREATE TABLE IF NOT EXISTS reporting.dim_purse (
    purse_sk                BIGSERIAL       PRIMARY KEY,
    -- FK to card
    fk_card                 BIGINT          REFERENCES reporting.dim_card(card_sk),
    -- FIS purse identifiers
    fis_purse_number        SMALLINT,       -- FIS internal slot (Monetary XTRACT field 60)
    fis_purse_name          VARCHAR(7)      NOT NULL,   -- e.g. OTC2550, FOD2550
    purse_type              CHAR(3)         GENERATED ALWAYS AS (LEFT(fis_purse_name, 3)) STORED,
                            -- OTC | FOD | CMB | REW | CSH — primary Tableau filter key
    purse_code              VARCHAR(30),    -- NULL — DM-01 blocker (health-plan namespace)
    -- Purse state
    purse_status            VARCHAR(15)     NOT NULL DEFAULT 'PENDING',
                            -- PENDING | ACTIVE | SUSPENDED | EXPIRED | CLOSED
    -- Benefit period
    effective_date          DATE            NOT NULL,
    expiration_date         DATE            NOT NULL,
                            -- Contractual 11:59 PM ET month-end (SOW §2.1 / §3.8)
    -- ACC config
    max_value               NUMERIC(12,2),
    max_load                NUMERIC(12,2),
    autoload_amount         NUMERIC(10,2),  -- ACC AutoLoadAmount; NULL if not configured
    -- APL linkage
    mcc_group               INTEGER,        -- FIS MCCGroup restriction ID
    iias_group              INTEGER,        -- FIS IIASGroup IIAS/APL category ID
    -- Dates
    purse_creation_date     DATE,
    purse_status_date       DATE,
    created_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS dim_purse_card
    ON reporting.dim_purse (fk_card);
CREATE INDEX IF NOT EXISTS dim_purse_type_period
    ON reporting.dim_purse (purse_type, effective_date);
CREATE INDEX IF NOT EXISTS dim_purse_fis_number
    ON reporting.dim_purse (fis_purse_number)
    WHERE fis_purse_number IS NOT NULL;

COMMENT ON TABLE reporting.dim_purse IS
'Purse dimension. Grain: one row per card-purse-benefit-period.
purse_type is GENERATED (LEFT(fis_purse_name,3)) — the canonical Tableau
filter for OTC vs FOD benefit aggregation (§4.4.5.3, §4.3.3).
purse_code is NULL pending DM-01 resolution (John Stevens).
expiration_date encodes the contractual 11:59 PM ET month-end deadline (SOW §2.1).';

-- ─── dim_program ─────────────────────────────────────────────────────────────
-- Source: CCX Data XTRACT (Client Configuration v1.17) + ACC provisioning.
-- Grain: one row per benefit program configuration.
-- Natural key: (tenant_id, fis_subprogram_id) — maps to SubprogramID across
-- all XTRACT feeds (CCX, Monetary, Auth, Non-Monetary, Filtered Spend).
CREATE TABLE IF NOT EXISTS reporting.dim_program (
    dim_program_sk          BIGSERIAL       PRIMARY KEY,
    tenant_id               TEXT            NOT NULL,
    -- FIS identifiers
    fis_client_id           NUMERIC(10),    -- CCX field 3: Issuer ClientID
    fis_program_id          NUMERIC(10),    -- CCX field 5: Program ID (parent)
    fis_subprogram_id       NUMERIC(10)     NOT NULL,   -- CCX field 2: natural key
    subprogram_name         VARCHAR(80),    -- CCX field 3: Sub Program Name
    fis_pack_id             TEXT,           -- Selvi Marappan owns provisioning (OI #1)
    -- Program metadata
    is_active               BOOLEAN         NOT NULL DEFAULT TRUE,  -- CCX field 4
    benefit_type            TEXT,           -- OTC | FOD | CMB
    apl_version             TEXT,           -- active APL version string for this subprogram
    -- Dates
    ccx_effective_date      DATE,           -- date CCX delta last updated this row
    created_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, fis_subprogram_id)
);

CREATE INDEX IF NOT EXISTS dim_program_subprogram
    ON reporting.dim_program (fis_subprogram_id);

COMMENT ON TABLE reporting.dim_program IS
'Program configuration dimension. Source: CCX XTRACT v1.17 + ACC provisioning.
Natural key: (tenant_id, fis_subprogram_id) — joins all XTRACT feeds.
Selvi Marappan owns fis_pack_id provisioning (OI #1). §4.3.5.';

-- ─── dim_txn_type ────────────────────────────────────────────────────────────
-- Reference table for FIS TxnTypeCode (Monetary XTRACT field 19,
-- Auth XTRACT field 13). Populated once at schema init; updated when
-- FIS publishes new type codes. Never written by MartWriter — static reference.
CREATE TABLE IF NOT EXISTS reporting.dim_txn_type (
    txn_type_code           SMALLINT        PRIMARY KEY,
    txn_type_name           VARCHAR(80)     NOT NULL,
    txn_category            TEXT            NOT NULL
                            CHECK (txn_category IN ('LOAD','PURCHASE','ATM','OTC','FEE','OTHER'))
);

COMMENT ON TABLE reporting.dim_txn_type IS
'FIS transaction type code reference table. §4.3.11.
Populated at schema init; never written by MartWriter.
Source: FIS Transaction Type Codes reference (Monetary XTRACT field 19).';

-- Seed known FIS TxnTypeCodes relevant to One Fintech (§4.3.11)
INSERT INTO reporting.dim_txn_type (txn_type_code, txn_type_name, txn_category) VALUES
    (1,  'ValueLoad',                   'LOAD'),
    (7,  'Adjustment',                  'LOAD'),
    (8,  'Adjustment Purse',            'LOAD'),
    (9,  'NonMonetary',                 'OTHER'),
    (10, 'Purchase Approved Settled',   'PURCHASE'),
    (11, 'Purchase Approved Pending',   'PURCHASE'),
    (12, 'Purchase Reversed',           'PURCHASE'),
    (13, 'Purchase Declined',           'PURCHASE'),
    (14, 'Purchase Refund',             'PURCHASE'),
    (15, 'Purchase Adjustment',         'PURCHASE'),
    (16, 'Purchase Auth Only',          'PURCHASE'),
    (17, 'Purchase Auth Reversal',      'PURCHASE'),
    (18, 'Purchase Auth Expiry',        'PURCHASE'),
    (20, 'ATM Withdrawal',              'ATM'),
    (21, 'ATM Reversed',                'ATM'),
    (22, 'ATM Declined',                'ATM'),
    (90, 'OTC Approved',                'OTC'),
    (91, 'OTC Reversed',                'OTC'),
    (92, 'OTC Declined',                'OTC'),
    (93, 'OTC Partial Approval',        'OTC'),
    (94, 'OTC Auth Only',               'OTC'),
    (95, 'OTC Auth Reversal',           'OTC'),
    (96, 'OTC Auth Expiry',             'OTC'),
    (97, 'OTC Refund',                  'OTC'),
    (99, 'LoadDecline',                 'LOAD')
ON CONFLICT (txn_type_code) DO NOTHING;
