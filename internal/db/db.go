package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"multi-tenant-scheduler/internal/models"
)

var conn *sql.DB

func Connect() error {
	dsn := os.Getenv("DB_DSN")
	var err error
	conn, err = sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	return conn.Ping()
}

func Ping(ctx context.Context) error {
	return conn.PingContext(ctx)
}

func Migrate(ctx context.Context) error {
	// Check if we're already on the new schema (tenant table exists).
	// If not, drop the old tenant_config table and create both new tables.
	var newSchemaExists bool
	conn.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema='public' AND table_name='tenant'
		)
	`).Scan(&newSchemaExists)

	if !newSchemaExists {
		if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS tenant_config CASCADE`); err != nil {
			return fmt.Errorf("drop old schema: %w", err)
		}
	}

	_, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS tenant (
			tenant_id    VARCHAR(50)  PRIMARY KEY,
			pms_provider VARCHAR(100) NOT NULL,
			created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
		);
		CREATE TABLE IF NOT EXISTS tenant_config (
			tenant_id  VARCHAR(50) PRIMARY KEY REFERENCES tenant(tenant_id) ON DELETE CASCADE,
			config     JSONB       NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`)
	return err
}

func CreateTenant(ctx context.Context, tenantID, pmsProvider string, cfg models.ScheduleConfig) error {
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO tenant (tenant_id, pms_provider) VALUES ($1, $2)`,
		tenantID, pmsProvider,
	); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO tenant_config (tenant_id, config) VALUES ($1, $2)`,
		tenantID, cfgJSON,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func GetTenant(ctx context.Context, tenantID string) (*models.TenantWithConfig, error) {
	var t models.TenantWithConfig
	var cfgJSON []byte
	err := conn.QueryRowContext(ctx, `
		SELECT t.tenant_id, t.pms_provider, t.created_at, tc.config, tc.updated_at
		FROM tenant t
		JOIN tenant_config tc ON t.tenant_id = tc.tenant_id
		WHERE t.tenant_id = $1
	`, tenantID).Scan(&t.TenantID, &t.PMSProvider, &t.CreatedAt, &cfgJSON, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(cfgJSON, &t.Config); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &t, nil
}

func ListTenants(ctx context.Context) ([]*models.TenantWithConfig, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT t.tenant_id, t.pms_provider, t.created_at, tc.config, tc.updated_at
		FROM tenant t
		JOIN tenant_config tc ON t.tenant_id = tc.tenant_id
		ORDER BY t.tenant_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*models.TenantWithConfig
	for rows.Next() {
		var t models.TenantWithConfig
		var cfgJSON []byte
		if err := rows.Scan(&t.TenantID, &t.PMSProvider, &t.CreatedAt, &cfgJSON, &t.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(cfgJSON, &t.Config); err != nil {
			return nil, fmt.Errorf("unmarshal config: %w", err)
		}
		list = append(list, &t)
	}
	return list, nil
}

func UpdateTenantConfig(ctx context.Context, tenantID string, cfg models.ScheduleConfig) error {
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	result, err := conn.ExecContext(ctx, `
		UPDATE tenant_config SET config=$1, updated_at=NOW() WHERE tenant_id=$2
	`, cfgJSON, tenantID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func DeleteTenant(ctx context.Context, tenantID string) error {
	_, err := conn.ExecContext(ctx, `DELETE FROM tenant WHERE tenant_id=$1`, tenantID)
	return err
}
