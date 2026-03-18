package aurora

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/_shared/ports"
)

// AuditLogRepo implements ports.AuditLogWriter against Aurora.
//
// INSERT only. The ingest_task_role is GRANTED INSERT and REVOKED UPDATE/DELETE
// at the DB layer — enforced via PostgreSQL permissions, not application convention (§4.2.7).
// Required for HIPAA §164.312(b) and PCI DSS 4.0 Req 10.2.
//
// Uses BIGSERIAL PK (not UUID) — write volume is high and sequential integer keys
// are more index-efficient at audit_log cardinality (§4.2.7).
type AuditLogRepo struct {
	pool *pgxpool.Pool
}

func NewAuditLogRepo(pool *pgxpool.Pool) *AuditLogRepo {
	return &AuditLogRepo{pool: pool}
}

func (r *AuditLogRepo) Write(ctx context.Context, e *ports.AuditEntry) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO public.audit_log (
			tenant_id, entity_type, entity_id,
			old_state, new_state,
			changed_by, correlation_id, client_member_id,
			fis_result_code, notes
		) VALUES (
			$1, $2, $3,
			$4, $5,
			$6, $7, $8,
			$9, $10
		)`,
		e.TenantID, e.EntityType, e.EntityID,
		e.OldState, e.NewState,
		e.ChangedBy, e.CorrelationID, e.ClientMemberID,
		e.FISResultCode, e.Notes,
	)
	if err != nil {
		return fmt.Errorf("audit_log.Write: %w", err)
	}
	return nil
}
