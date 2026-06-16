package db

import (
	"context"
	"database/sql"
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
		CREATE TABLE IF NOT EXISTS tenant_config (
			tenant_id   VARCHAR(50)   PRIMARY KEY,
			name        VARCHAR(255)  NOT NULL,
			timezone    VARCHAR(100)  NOT NULL DEFAULT 'UTC',
			run_time    VARCHAR(5)    NOT NULL DEFAULT '',
			enabled     BOOLEAN       NOT NULL DEFAULT true,
			latitude    DECIMAL(9,6)  NOT NULL DEFAULT 0,
			longitude   DECIMAL(9,6)  NOT NULL DEFAULT 0,
			hotel_code  VARCHAR(50),
			created_at  TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
			updated_at  TIMESTAMPTZ   NOT NULL DEFAULT NOW()
		);
		ALTER TABLE tenant_config ADD COLUMN IF NOT EXISTS schedule_type VARCHAR(10) NOT NULL DEFAULT 'daily';
		ALTER TABLE tenant_config ADD COLUMN IF NOT EXISTS rate_minutes  INT         NOT NULL DEFAULT 0;
	`)
	return err
}

func CreateTenant(ctx context.Context, t *models.Tenant) error {
	_, err := conn.ExecContext(ctx, `
		INSERT INTO tenant_config
			(tenant_id, name, schedule_type, timezone, run_time, rate_minutes, enabled, latitude, longitude, hotel_code)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, t.TenantID, t.Name, t.ScheduleType, t.Timezone, t.RunTime, t.RateMinutes, t.Enabled,
		t.Latitude, t.Longitude, t.HotelCode)
	return err
}

func GetTenant(ctx context.Context, tenantID string) (*models.Tenant, error) {
	t := &models.Tenant{}
	err := conn.QueryRowContext(ctx, `
		SELECT tenant_id, name, schedule_type, timezone, run_time, rate_minutes, enabled,
		       latitude, longitude, hotel_code, created_at, updated_at
		FROM tenant_config WHERE tenant_id = $1
	`, tenantID).Scan(
		&t.TenantID, &t.Name, &t.ScheduleType, &t.Timezone, &t.RunTime, &t.RateMinutes, &t.Enabled,
		&t.Latitude, &t.Longitude, &t.HotelCode, &t.CreatedAt, &t.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

func ListTenants(ctx context.Context) ([]*models.Tenant, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT tenant_id, name, schedule_type, timezone, run_time, rate_minutes, enabled,
		       latitude, longitude, hotel_code, created_at, updated_at
		FROM tenant_config ORDER BY tenant_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*models.Tenant
	for rows.Next() {
		t := &models.Tenant{}
		if err := rows.Scan(
			&t.TenantID, &t.Name, &t.ScheduleType, &t.Timezone, &t.RunTime, &t.RateMinutes, &t.Enabled,
			&t.Latitude, &t.Longitude, &t.HotelCode, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, t)
	}
	return list, nil
}

func UpdateTenantSchedule(ctx context.Context, tenantID, scheduleType, timezone, runTime string, rateMinutes int, enabled bool) error {
	_, err := conn.ExecContext(ctx, `
		UPDATE tenant_config
		SET schedule_type=$1, timezone=$2, run_time=$3, rate_minutes=$4, enabled=$5, updated_at=NOW()
		WHERE tenant_id=$6
	`, scheduleType, timezone, runTime, rateMinutes, enabled, tenantID)
	return err
}

func DeleteTenant(ctx context.Context, tenantID string) error {
	_, err := conn.ExecContext(ctx, `DELETE FROM tenant_config WHERE tenant_id=$1`, tenantID)
	return err
}
