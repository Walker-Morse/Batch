-- One Fintech Platform — dim_date Population
-- §4.3.4 dim_date: standard date dimension covering 2020–2035.
-- Grain: one row per calendar day. Populated once at schema initialisation;
-- never updated by pipeline. Integer surrogate key = YYYYMMDD format.
-- Fiscal year columns left NULL pending DM-04 resolution
-- (confirm calendar-year offset with Kendra Williams for RFU).

CREATE TABLE IF NOT EXISTS reporting.dim_date (
    date_sk         INTEGER     PRIMARY KEY,    -- YYYYMMDD integer
    full_date       DATE        NOT NULL UNIQUE,
    year            SMALLINT    NOT NULL,
    quarter         SMALLINT    NOT NULL CHECK (quarter BETWEEN 1 AND 4),
    month           SMALLINT    NOT NULL CHECK (month BETWEEN 1 AND 12),
    month_name      VARCHAR(9)  NOT NULL,
    week_of_year    SMALLINT    NOT NULL,       -- ISO week 1–53
    day_of_week     SMALLINT    NOT NULL,       -- 0=Sunday .. 6=Saturday
    day_name        VARCHAR(9)  NOT NULL,
    is_weekend      BOOLEAN     NOT NULL,
    benefit_period  CHAR(7)     NOT NULL,       -- ISO YYYY-MM; primary Tableau group-by field
    fiscal_year     SMALLINT,                   -- NULL until DM-04 resolved
    fiscal_quarter  SMALLINT                    -- NULL until DM-04 resolved
);

COMMENT ON TABLE reporting.dim_date IS
'Standard calendar date dimension 2020–2035. §4.3.4.
Populated once at schema init; never written by pipeline.
date_sk = YYYYMMDD integer — join key on all fact tables.
benefit_period = ISO YYYY-MM; the primary Tableau month group-by field.
fiscal_year/fiscal_quarter NULL until DM-04 resolved (Kendra Williams confirms offset).';

COMMENT ON COLUMN reporting.dim_date.benefit_period IS
'ISO YYYY-MM string. The primary Tableau group-by for monthly benefit analysis.
Joining dim_date means analysts never need EXTRACT(MONTH FROM ...) SQL — they
drag benefit_period from the dimension list directly (§4.4.5.2).';

-- ─── Population ──────────────────────────────────────────────────────────────
-- Covers 2020-01-01 through 2035-12-31 (5,844 rows).
-- Uses a generate_series to avoid a static values insert.
-- ON CONFLICT DO NOTHING makes this re-runnable safely.
INSERT INTO reporting.dim_date (
    date_sk,
    full_date,
    year,
    quarter,
    month,
    month_name,
    week_of_year,
    day_of_week,
    day_name,
    is_weekend,
    benefit_period
)
SELECT
    TO_CHAR(d, 'YYYYMMDD')::INTEGER             AS date_sk,
    d                                           AS full_date,
    EXTRACT(YEAR    FROM d)::SMALLINT           AS year,
    EXTRACT(QUARTER FROM d)::SMALLINT           AS quarter,
    EXTRACT(MONTH   FROM d)::SMALLINT           AS month,
    TO_CHAR(d, 'Month')                         AS month_name,  -- 'January   ' — Tableau trims
    EXTRACT(ISODOW  FROM d)::SMALLINT           AS week_of_year,
    -- day_of_week: 0=Sunday per HLD spec
    -- PostgreSQL DOW: 0=Sunday, 6=Saturday — matches spec directly
    EXTRACT(DOW     FROM d)::SMALLINT           AS day_of_week,
    TO_CHAR(d, 'Day')                           AS day_name,
    EXTRACT(DOW     FROM d) IN (0, 6)           AS is_weekend,
    TO_CHAR(d, 'YYYY-MM')                       AS benefit_period
FROM generate_series(
    '2020-01-01'::DATE,
    '2035-12-31'::DATE,
    '1 day'::INTERVAL
) AS d
ON CONFLICT (date_sk) DO NOTHING;

-- ─── Index ───────────────────────────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS dim_date_full_date
    ON reporting.dim_date (full_date);
CREATE INDEX IF NOT EXISTS dim_date_benefit_period
    ON reporting.dim_date (benefit_period);
CREATE INDEX IF NOT EXISTS dim_date_year_month
    ON reporting.dim_date (year, month);
