BEGIN;

-- Keep fixture loads deterministic for local-dev tenant.
DELETE FROM public.purses WHERE tenant_id = 'local-dev';
DELETE FROM public.cards WHERE tenant_id = 'local-dev';
DELETE FROM public.consumers WHERE tenant_id = 'local-dev';
DELETE FROM public.batch_records_rt60 WHERE tenant_id = 'local-dev';
DELETE FROM public.batch_records_rt37 WHERE tenant_id = 'local-dev';
DELETE FROM public.batch_records_rt30 WHERE tenant_id = 'local-dev';
DELETE FROM public.domain_commands WHERE tenant_id = 'local-dev';
DELETE FROM public.batch_files WHERE tenant_id = 'local-dev';
DELETE FROM public.programs WHERE tenant_id = 'local-dev';

INSERT INTO public.programs (
  id, tenant_id, fis_client_id, fis_program_id, fis_subprogram_id,
  subprogram_name, fis_pack_id, package_id, client_name, benefit_type, state
) VALUES
  ('10000000-0000-0000-0000-000000000001', 'local-dev', 26001, 26001, 26071,
   'Local OTC Program', 'OTC2550', 'PKG-OTC-001', 'Local Client', 'OTC', 'OR'),
  ('10000000-0000-0000-0000-000000000002', 'local-dev', 26001, 26002, 26072,
   'Local FOD Program', 'FOD2550', 'PKG-FOD-001', 'Local Client', 'FOD', 'OR');

INSERT INTO public.batch_files (
  id, correlation_id, tenant_id, client_id, file_type, status, record_count, malformed_count
) VALUES
  ('20000000-0000-0000-0000-000000000001', '30000000-0000-0000-0000-000000000001',
   'local-dev', 'local-client', 'SRG310', 'COMPLETE', 2, 0);

INSERT INTO public.domain_commands (
  id, correlation_id, tenant_id, client_member_id, command_type,
  benefit_period, status, batch_file_id, sequence_in_file
) VALUES
  ('40000000-0000-0000-0000-000000000001', '30000000-0000-0000-0000-000000000001',
   'local-dev', 'LOCAL-0001', 'ENROLL', '2026-03', 'Completed',
   '20000000-0000-0000-0000-000000000001', 1),
  ('40000000-0000-0000-0000-000000000002', '30000000-0000-0000-0000-000000000001',
   'local-dev', 'LOCAL-0002', 'ENROLL', '2026-03', 'Completed',
   '20000000-0000-0000-0000-000000000001', 2);

INSERT INTO public.batch_records_rt30 (
  id, batch_file_id, domain_command_id, correlation_id, tenant_id, sequence_in_file,
  status, fis_result_code, fis_result_message, at_code, client_member_id, subprogram_id,
  package_id, first_name, last_name, date_of_birth, address_1, city, state, zip, email,
  card_design_id, custom_card_id, fis_person_id, fis_cuid, fis_card_id, raw_payload, program_id
) VALUES
  ('50000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000001',
   '40000000-0000-0000-0000-000000000001', '30000000-0000-0000-0000-000000000001',
   'local-dev', 1, 'COMPLETED', '000', 'approved', 'AT01', 'LOCAL-0001', 26071, 'PKG-OTC-001',
   'Jane', 'Tester', '1985-06-15', '123 Local Ave', 'Portland', 'OR', '97201', 'jane@example.test',
   'DESIGN1', 'CARD-LOCAL-0001', 'P0000000001', 'C0000000001', '4111111111110001',
   '{"source":"fixture","row":1}'::jsonb, '10000000-0000-0000-0000-000000000001'),
  ('50000000-0000-0000-0000-000000000002', '20000000-0000-0000-0000-000000000001',
   '40000000-0000-0000-0000-000000000002', '30000000-0000-0000-0000-000000000001',
   'local-dev', 2, 'COMPLETED', '000', 'approved', 'AT01', 'LOCAL-0002', 26072, 'PKG-FOD-001',
   'John', 'Example', '1990-01-20', '456 Dev St', 'Portland', 'OR', '97202', 'john@example.test',
   'DESIGN1', 'CARD-LOCAL-0002', 'P0000000002', 'C0000000002', '4111111111110002',
   '{"source":"fixture","row":2}'::jsonb, '10000000-0000-0000-0000-000000000002');

INSERT INTO public.consumers (
  id, tenant_id, client_member_id, status, fis_person_id, fis_cuid,
  first_name, last_name, date_of_birth, address_1, city, state, zip, email,
  program_id, subprogram_id, source_batch_file_id
) VALUES
  ('60000000-0000-0000-0000-000000000001', 'local-dev', 'LOCAL-0001', 'ACTIVE',
   'P0000000001', 'C0000000001',
   'Jane', 'Tester', '1985-06-15', '123 Local Ave', 'Portland', 'OR', '97201', 'jane@example.test',
   '10000000-0000-0000-0000-000000000001', 26071, '20000000-0000-0000-0000-000000000001'),
  ('60000000-0000-0000-0000-000000000002', 'local-dev', 'LOCAL-0002', 'ACTIVE',
   'P0000000002', 'C0000000002',
   'John', 'Example', '1990-01-20', '456 Dev St', 'Portland', 'OR', '97202', 'john@example.test',
   '10000000-0000-0000-0000-000000000002', 26072, '20000000-0000-0000-0000-000000000001');

INSERT INTO public.cards (
  id, tenant_id, consumer_id, client_member_id, fis_card_id, pan_masked, fis_proxy_number,
  card_status, card_design_id, package_id, issued_at, source_batch_file_id
) VALUES
  ('70000000-0000-0000-0000-000000000001', 'local-dev', '60000000-0000-0000-0000-000000000001',
   'LOCAL-0001', '4111111111110001', '411111******0001', 'PX-LOCAL-0001',
   2, 'DESIGN1', 'PKG-OTC-001', NOW(), '20000000-0000-0000-0000-000000000001'),
  ('70000000-0000-0000-0000-000000000002', 'local-dev', '60000000-0000-0000-0000-000000000002',
   'LOCAL-0002', '4111111111110002', '411111******0002', 'PX-LOCAL-0002',
   2, 'DESIGN1', 'PKG-FOD-001', NOW(), '20000000-0000-0000-0000-000000000001');

INSERT INTO public.purses (
  id, tenant_id, card_id, consumer_id, client_member_id, fis_purse_number,
  fis_purse_name, status, available_balance_cents, benefit_period, effective_date, expiry_date,
  program_id, benefit_type, activated_at, source_batch_file_id
) VALUES
  ('80000000-0000-0000-0000-000000000001', 'local-dev', '70000000-0000-0000-0000-000000000001',
   '60000000-0000-0000-0000-000000000001', 'LOCAL-0001', 1,
   'OTC2550', 'ACTIVE', 9500, '2026-03', '2026-03-01', '2026-03-31',
   '10000000-0000-0000-0000-000000000001', 'OTC', NOW(), '20000000-0000-0000-0000-000000000001'),
  ('80000000-0000-0000-0000-000000000002', 'local-dev', '70000000-0000-0000-0000-000000000002',
   '60000000-0000-0000-0000-000000000002', 'LOCAL-0002', 2,
   'FOD2550', 'ACTIVE', 25000, '2026-03', '2026-03-01', '2026-03-31',
   '10000000-0000-0000-0000-000000000002', 'FOD', NOW(), '20000000-0000-0000-0000-000000000001');

COMMIT;
