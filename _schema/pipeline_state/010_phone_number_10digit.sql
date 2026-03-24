-- Migration 010: enforce 9-digit constraint on batch_records_rt30.phone_number
-- phone_number is digits-only, 10 digits exactly, or 0 (not provided).
-- 0 sentinel is preserved for optional field; any non-zero value must be
-- exactly 10 digits (1000000000–9999999999).
ALTER TABLE public.batch_records_rt30
    ADD CONSTRAINT chk_phone_number_10digit
    CHECK (phone_number = 0 OR (phone_number >= 1000000000 AND phone_number <= 9999999999));

COMMENT ON COLUMN public.batch_records_rt30.phone_number IS
'SRG310 phone_number field. Digits only, exactly 10 digits when provided, 0 when absent.
Constraint: 0 (not provided) OR 1000000000-9999999999 (10 digits exactly).
FIS RT30 field offset 270, width 9. Confirm offset with Kendra (Open Item #9).';
