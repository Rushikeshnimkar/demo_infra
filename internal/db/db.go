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
	_, err := conn.ExecContext(ctx, `
		DROP TABLE IF EXISTS tenant_config CASCADE;
		CREATE TABLE IF NOT EXISTS tenant (
			tenant_id    VARCHAR(50)  PRIMARY KEY,
			pms_provider VARCHAR(100) NOT NULL,
			config       JSONB        NOT NULL DEFAULT '{}',
			created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
			updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
		);
	`)
	return err
}

func CreateTenant(ctx context.Context, tenantID, pmsProvider string, config json.RawMessage) error {
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	_, err := conn.ExecContext(ctx,
		`INSERT INTO tenant (tenant_id, pms_provider, config) VALUES ($1, $2, $3)`,
		tenantID, pmsProvider, []byte(config),
	)
	return err
}

func UpdateConfig(ctx context.Context, tenantID string, config json.RawMessage) error {
	_, err := conn.ExecContext(ctx,
		`UPDATE tenant SET config = $2, updated_at = NOW() WHERE tenant_id = $1`,
		tenantID, []byte(config),
	)
	return err
}

func GetTenant(ctx context.Context, tenantID string) (*models.Tenant, error) {
	var t models.Tenant
	var config []byte
	err := conn.QueryRowContext(ctx,
		`SELECT tenant_id, pms_provider, config, created_at FROM tenant WHERE tenant_id = $1`,
		tenantID,
	).Scan(&t.TenantID, &t.PMSProvider, &config, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Config = json.RawMessage(config)
	return &t, nil
}

func ListTenants(ctx context.Context) ([]*models.Tenant, error) {
	rows, err := conn.QueryContext(ctx,
		`SELECT tenant_id, pms_provider, config, created_at FROM tenant ORDER BY tenant_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*models.Tenant
	for rows.Next() {
		var t models.Tenant
		var config []byte
		if err := rows.Scan(&t.TenantID, &t.PMSProvider, &config, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Config = json.RawMessage(config)
		list = append(list, &t)
	}
	return list, nil
}

func DeleteTenant(ctx context.Context, tenantID string) error {
	_, err := conn.ExecContext(ctx, `DELETE FROM tenant WHERE tenant_id = $1`, tenantID)
	return err
}
