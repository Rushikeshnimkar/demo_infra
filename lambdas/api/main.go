package main

import (
	"context"
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

// validateSchedule checks that the schedule fields are consistent for the given type.
func validateSchedule(scheduleType, timezone, runTime string, rateMinutes int) string {
	switch scheduleType {
	case "daily":
		if timezone == "" || runTime == "" {
			return "timezone and runTime are required for daily schedules"
		}
	case "rate":
		if rateMinutes <= 0 {
			return "rateMinutes must be greater than 0 for rate schedules"
		}
	default:
		return "scheduleType must be 'daily' or 'rate'"
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
		} else {
			if tenants == nil {
				tenants = []*models.Tenant{}
			}
			logger.Info("db: tenants listed", "count", len(tenants))
			resp, err = respond(200, tenants)
		}
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
		if r.ScheduleType == "" {
			r.ScheduleType = "daily"
		}
		if msg := validateSchedule(r.ScheduleType, r.Timezone, r.RunTime, r.RateMinutes); msg != "" {
			resp, err = errResp(400, msg)
			goto done
		}

		t := &models.Tenant{
			TenantID:     r.TenantID,
			Name:         r.Name,
			ScheduleType: r.ScheduleType,
			Timezone:     r.Timezone,
			RunTime:      r.RunTime,
			RateMinutes:  r.RateMinutes,
			Enabled:      true,
			Latitude:     r.Latitude,
			Longitude:    r.Longitude,
			HotelCode:    r.HotelCode,
		}
		if dbErr := db.CreateTenant(ctx, t); dbErr != nil {
			logger.Error("db: create tenant failed", "tenantId", r.TenantID, "error", dbErr)
			resp, err = errResp(500, dbErr.Error())
			goto done
		}
		logger.Info("db: tenant created", "tenantId", r.TenantID, "name", r.Name)

		if schedErr := sched.Upsert(ctx, r.TenantID, r.ScheduleType, r.Timezone, r.RunTime, r.RateMinutes); schedErr != nil {
			logger.Error("scheduler: upsert failed", "tenantId", r.TenantID, "error", schedErr)
			resp, err = errResp(500, fmt.Sprintf("scheduler: %v", schedErr))
			goto done
		}
		logger.Info("scheduler: created",
			"tenantId", r.TenantID,
			"scheduleType", r.ScheduleType,
			"timezone", r.Timezone,
			"runTime", r.RunTime,
			"rateMinutes", r.RateMinutes,
		)
		resp, err = respond(201, t)
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
				logger.Warn("db: tenant not found", "tenantId", tenantID)
				resp, err = errResp(404, "tenant not found")
				goto done
			}
			logger.Info("db: tenant fetched", "tenantId", tenantID)
			resp, err = respond(200, t)
			goto done
		}

		// PUT /tenants/{id}/schedule
		if method == http.MethodPut && len(parts) == 3 && parts[2] == "schedule" {
			var r models.UpdateScheduleRequest
			if jsonErr := json.Unmarshal([]byte(req.Body), &r); jsonErr != nil {
				resp, err = errResp(400, "invalid request body")
				goto done
			}
			if r.ScheduleType == "" {
				r.ScheduleType = "daily"
			}
			if msg := validateSchedule(r.ScheduleType, r.Timezone, r.RunTime, r.RateMinutes); msg != "" {
				resp, err = errResp(400, msg)
				goto done
			}
			enabled := true
			if r.Enabled != nil {
				enabled = *r.Enabled
			}
			if dbErr := db.UpdateTenantSchedule(ctx, tenantID, r.ScheduleType, r.Timezone, r.RunTime, r.RateMinutes, enabled); dbErr != nil {
				logger.Error("db: update schedule failed", "tenantId", tenantID, "error", dbErr)
				resp, err = errResp(500, dbErr.Error())
				goto done
			}
			logger.Info("db: schedule updated",
				"tenantId", tenantID,
				"scheduleType", r.ScheduleType,
				"timezone", r.Timezone,
				"runTime", r.RunTime,
				"rateMinutes", r.RateMinutes,
				"enabled", enabled,
			)
			if enabled {
				if schedErr := sched.Upsert(ctx, tenantID, r.ScheduleType, r.Timezone, r.RunTime, r.RateMinutes); schedErr != nil {
					logger.Error("scheduler: upsert failed", "tenantId", tenantID, "error", schedErr)
					resp, err = errResp(500, fmt.Sprintf("scheduler upsert: %v", schedErr))
					goto done
				}
				logger.Info("scheduler: updated",
					"tenantId", tenantID,
					"scheduleType", r.ScheduleType,
					"timezone", r.Timezone,
					"runTime", r.RunTime,
					"rateMinutes", r.RateMinutes,
				)
			} else {
				if schedErr := sched.Disable(ctx, tenantID); schedErr != nil {
					logger.Error("scheduler: disable failed", "tenantId", tenantID, "error", schedErr)
					resp, err = errResp(500, fmt.Sprintf("scheduler disable: %v", schedErr))
					goto done
				}
				logger.Info("scheduler: disabled", "tenantId", tenantID)
			}
			resp, err = respond(200, map[string]string{"status": "updated"})
			goto done
		}

		// DELETE /tenants/{id}
		if method == http.MethodDelete && len(parts) == 2 {
			if dbErr := db.DeleteTenant(ctx, tenantID); dbErr != nil {
				logger.Error("db: delete tenant failed", "tenantId", tenantID, "error", dbErr)
				resp, err = errResp(500, dbErr.Error())
				goto done
			}
			logger.Info("db: tenant deleted", "tenantId", tenantID)
			if schedErr := sched.Delete(ctx, tenantID); schedErr != nil {
				logger.Error("scheduler: delete failed", "tenantId", tenantID, "error", schedErr)
				resp, err = errResp(500, fmt.Sprintf("delete scheduler: %v", schedErr))
				goto done
			}
			logger.Info("scheduler: deleted", "tenantId", tenantID)
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
