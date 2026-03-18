// Package aurora provides a pgx connection pool factory for Aurora PostgreSQL Serverless v2.
//
// Architecture (ADR-008, ADR-009):
//   - Aurora Serverless v2 per tenant database
//   - RDS Proxy provides connection stability — pool connects through the proxy endpoint
//   - TLS 1.2+ enforced on all connections (§5.4.2); rds.force_ssl = 1 on Aurora
//   - Credentials from Secrets Manager — never from environment variables (§5.4.5)
//
// RDS Proxy note: the pool is sized for one persistent connection per concurrent
// Fargate task. Do NOT use a large pool — RDS Proxy is sized per-task, not per-row.
// Row-level parallelism is explicitly prohibited without confirming pool sizing (ADR-010).
package aurora

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config holds the connection parameters for Aurora via RDS Proxy.
type Config struct {
	Host     string // RDS Proxy endpoint
	Port     int    // default 5432
	Database string // per-tenant database name
	User     string // ingest_task_role login account
	Password string // from Secrets Manager — never hardcoded
	// TLS is enforced; sslmode=require matches Aurora parameter rds.force_ssl=1 (§5.4.2)
	SSLMode string // "require" for TST/PRD; "disable" only for local dev
}

// NewPool creates a pgxpool connecting through RDS Proxy with TLS enforced.
// Caller is responsible for calling pool.Close() on shutdown.
func NewPool(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	if cfg.Port == 0 {
		cfg.Port = 5432
	}
	if cfg.SSLMode == "" {
		cfg.SSLMode = "require"
	}

	dsn := fmt.Sprintf(
		"host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.Database, cfg.User, cfg.Password, cfg.SSLMode,
	)

	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("aurora: parse config: %w", err)
	}

	// One persistent connection per Fargate task (ADR-010, RDS Proxy sizing constraint).
	// The ingest-task processes rows sequentially — it doesn't need a large pool.
	poolCfg.MaxConns = 2 // 1 active + 1 spare for audit_log writes
	poolCfg.MinConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("aurora: connect to %s/%s: %w", cfg.Host, cfg.Database, err)
	}

	// Verify the connection immediately — fail fast on misconfiguration
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("aurora: ping %s/%s: %w", cfg.Host, cfg.Database, err)
	}

	return pool, nil
}
