package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"multi-tenant-scheduler/internal/db"
	"multi-tenant-scheduler/internal/models"
	sched "multi-tenant-scheduler/internal/scheduler"
)

func init() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	if err := db.Connect(); err != nil {
		slog.Error("startup failed: db connect", "error", err)
		os.Exit(1)
	}
	if err := db.Migrate(context.Background()); err != nil {
		slog.Error("startup failed: db migrate", "error", err)
		os.Exit(1)
	}
	slog.Info("startup complete: db connected and schema ready")
}

func respond(status int, body any) (events.APIGatewayV2HTTPResponse, error) {
	b, _ := json.Marshal(body)
	return events.APIGatewayV2HTTPResponse{
		StatusCode: status,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       string(b),
	}, nil
}

func errResp(status int, msg string) (events.APIGatewayV2HTTPResponse, error) {
	return respond(status, map[string]string{"error": msg})
}

func validateConfig(cfg models.ScheduleConfig) string {
	if cfg.Name == "" {
		return "config.name is required"
	}
	if cfg.Timezone == "" {
		return "config.timezone is required"
	}
	if cfg.Cron == "" {
		return "config.cron is required"
	}
	return ""
}

func handler(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	start := time.Now()
	method := req.RequestContext.HTTP.Method
	path := strings.TrimRight(req.RequestContext.HTTP.Path, "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	reqID := req.RequestContext.RequestID

	logger := slog.Default().With("requestId", reqID)
	logger.Info("request received", "method", method, "path", path)

	var resp events.APIGatewayV2HTTPResponse
	var err error

	// GET /health
	if method == http.MethodGet && path == "/health" {
		if pingErr := db.Ping(ctx); pingErr != nil {
			logger.Error("health: db ping failed", "error", pingErr)
			resp, err = errResp(503, "database unavailable")
		} else {
			resp, err = respond(200, map[string]string{"status": "ok"})
		}
		goto done
	}

	// GET /tenants
	if method == http.MethodGet && path == "/tenants" {
		tenants, dbErr := db.ListTenants(ctx)
		if dbErr != nil {
			logger.Error("db: list tenants failed", "error", dbErr)
			resp, err = errResp(500, dbErr.Error())
			goto done
		}
		if tenants == nil {
			tenants = []*models.TenantWithConfig{}
		}
		resp, err = respond(200, tenants)
		goto done
	}

	// POST /tenants
	if method == http.MethodPost && path == "/tenants" {
		var r models.CreateTenantRequest
		if jsonErr := json.Unmarshal([]byte(req.Body), &r); jsonErr != nil {
			resp, err = errResp(400, "invalid request body")
			goto done
		}
		if r.TenantID == "" {
			resp, err = errResp(400, "tenantId is required")
			goto done
		}
		if r.PMSProvider == "" {
			resp, err = errResp(400, "pmsProvider is required")
			goto done
		}
		if msg := validateConfig(r.Config); msg != "" {
			resp, err = errResp(400, msg)
			goto done
		}

		// Schedule first, then DB (rollback scheduler on DB failure)
		if schedErr := sched.Upsert(ctx, r.Config.Name, r.Config.Timezone, r.Config.Cron, r.TenantID, r.Config.Enabled); schedErr != nil {
			logger.Error("scheduler: upsert failed", "tenantId", r.TenantID, "error", schedErr)
			resp, err = errResp(500, fmt.Sprintf("scheduler: %v", schedErr))
			goto done
		}
		logger.Info("scheduler: created", "tenantId", r.TenantID, "scheduleName", r.Config.Name)

		if dbErr := db.CreateTenant(ctx, r.TenantID, r.PMSProvider, r.Config); dbErr != nil {
			logger.Error("db: create tenant failed", "tenantId", r.TenantID, "error", dbErr)
			_ = sched.Delete(ctx, r.Config.Name)
			resp, err = errResp(500, dbErr.Error())
			goto done
		}
		logger.Info("db: tenant created", "tenantId", r.TenantID)

		resp, err = respond(201, models.TenantWithConfig{
			TenantID:    r.TenantID,
			PMSProvider: r.PMSProvider,
			Config:      r.Config,
		})
		goto done
	}

	if len(parts) < 2 || parts[0] != "tenants" {
		resp, err = errResp(404, "not found")
		goto done
	}

	{
		tenantID := parts[1]

		// GET /tenants/{id}
		if method == http.MethodGet && len(parts) == 2 {
			t, dbErr := db.GetTenant(ctx, tenantID)
			if dbErr != nil {
				logger.Error("db: get tenant failed", "tenantId", tenantID, "error", dbErr)
				resp, err = errResp(500, dbErr.Error())
				goto done
			}
			if t == nil {
				resp, err = errResp(404, "tenant not found")
				goto done
			}
			resp, err = respond(200, t)
			goto done
		}

		// PUT /tenants/{id}/config
		if method == http.MethodPut && len(parts) == 3 && parts[2] == "config" {
			var r models.UpdateConfigRequest
			if jsonErr := json.Unmarshal([]byte(req.Body), &r); jsonErr != nil {
				resp, err = errResp(400, "invalid request body")
				goto done
			}
			if msg := validateConfig(r.Config); msg != "" {
				resp, err = errResp(400, msg)
				goto done
			}

			// Fetch current config to detect schedule name change
			current, dbErr := db.GetTenant(ctx, tenantID)
			if dbErr != nil {
				logger.Error("db: get tenant failed", "tenantId", tenantID, "error", dbErr)
				resp, err = errResp(500, dbErr.Error())
				goto done
			}
			if current == nil {
				resp, err = errResp(404, "tenant not found")
				goto done
			}
			oldName := current.Config.Name

			// Schedule first, then DB
			if schedErr := sched.Upsert(ctx, r.Config.Name, r.Config.Timezone, r.Config.Cron, tenantID, r.Config.Enabled); schedErr != nil {
				logger.Error("scheduler: upsert failed", "tenantId", tenantID, "error", schedErr)
				resp, err = errResp(500, fmt.Sprintf("scheduler: %v", schedErr))
				goto done
			}
			logger.Info("scheduler: updated", "tenantId", tenantID, "scheduleName", r.Config.Name)

			// Delete old scheduler if the name changed
			if oldName != r.Config.Name {
				if schedErr := sched.Delete(ctx, oldName); schedErr != nil {
					logger.Error("scheduler: delete old name failed", "tenantId", tenantID, "oldName", oldName, "error", schedErr)
				} else {
					logger.Info("scheduler: old name deleted", "tenantId", tenantID, "oldName", oldName)
				}
			}

			if dbErr := db.UpdateTenantConfig(ctx, tenantID, r.Config); dbErr != nil {
				if dbErr == sql.ErrNoRows {
					resp, err = errResp(404, "tenant not found")
				} else {
					logger.Error("db: update config failed", "tenantId", tenantID, "error", dbErr)
					resp, err = errResp(500, dbErr.Error())
				}
				goto done
			}
			logger.Info("db: config updated", "tenantId", tenantID)
			resp, err = respond(200, map[string]string{"status": "updated"})
			goto done
		}

		// DELETE /tenants/{id}
		if method == http.MethodDelete && len(parts) == 2 {
			// Fetch config first to know the scheduler name
			t, dbErr := db.GetTenant(ctx, tenantID)
			if dbErr != nil {
				logger.Error("db: get tenant failed", "tenantId", tenantID, "error", dbErr)
				resp, err = errResp(500, dbErr.Error())
				goto done
			}
			if t == nil {
				resp, err = errResp(404, "tenant not found")
				goto done
			}
			scheduleName := t.Config.Name

			if dbErr := db.DeleteTenant(ctx, tenantID); dbErr != nil {
				logger.Error("db: delete tenant failed", "tenantId", tenantID, "error", dbErr)
				resp, err = errResp(500, dbErr.Error())
				goto done
			}
			logger.Info("db: tenant deleted", "tenantId", tenantID)

			if schedErr := sched.Delete(ctx, scheduleName); schedErr != nil {
				logger.Error("scheduler: delete failed", "tenantId", tenantID, "error", schedErr)
				resp, err = errResp(500, fmt.Sprintf("delete scheduler: %v", schedErr))
				goto done
			}
			logger.Info("scheduler: deleted", "tenantId", tenantID, "scheduleName", scheduleName)
			resp, err = respond(200, map[string]string{"status": "deleted"})
			goto done
		}
	}

	resp, err = errResp(404, "not found")

done:
	logger.Info("request complete",
		"method", method,
		"path", path,
		"status", resp.StatusCode,
		"durationMs", time.Since(start).Milliseconds(),
	)
	return resp, err
}

func main() {
	lambda.Start(handler)
}
