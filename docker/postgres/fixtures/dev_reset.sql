BEGIN;

DELETE FROM public.purses WHERE tenant_id = 'local-dev';
DELETE FROM public.cards WHERE tenant_id = 'local-dev';
DELETE FROM public.consumers WHERE tenant_id = 'local-dev';
DELETE FROM public.batch_records_rt60 WHERE tenant_id = 'local-dev';
DELETE FROM public.batch_records_rt37 WHERE tenant_id = 'local-dev';
DELETE FROM public.batch_records_rt30 WHERE tenant_id = 'local-dev';
DELETE FROM public.domain_commands WHERE tenant_id = 'local-dev';
DELETE FROM public.batch_files WHERE tenant_id = 'local-dev';
DELETE FROM public.programs WHERE tenant_id = 'local-dev';

COMMIT;
