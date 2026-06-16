package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"multi-tenant-scheduler/internal/db"
)

type TriggerEvent struct {
	TenantID string `json:"tenantId"`
}

type openMeteoResponse struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone"`
	Current   struct {
		Time        string  `json:"time"`
		Temperature float64 `json:"temperature_2m"`
		WindSpeed   float64 `json:"wind_speed_10m"`
		WeatherCode int     `json:"weather_code"`
	} `json:"current"`
}

type sqsPayload struct {
	TenantID    string            `json:"tenantId"`
	HotelCode   string            `json:"hotelCode"`
	ExecutedAt  string            `json:"executedAt"`
	WeatherData openMeteoResponse `json:"weatherData"`
}

var sqsClient *sqs.Client

func init() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	if err := db.Connect(); err != nil {
		slog.Error("startup failed: db connect", "error", err)
		os.Exit(1)
	}
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		slog.Error("startup failed: aws config", "error", err)
		os.Exit(1)
	}
	sqsClient = sqs.NewFromConfig(cfg)
}

func fetchWeather(lat, lon float64) (*openMeteoResponse, error) {
	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast"+
			"?latitude=%.6f&longitude=%.6f"+
			"&current=temperature_2m,wind_speed_10m,weather_code"+
			"&forecast_days=1",
		lat, lon,
	)
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("GET open-meteo: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var w openMeteoResponse
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("parse open-meteo response: %w", err)
	}
	return &w, nil
}

func handler(ctx context.Context, event TriggerEvent) error {
	start := time.Now()

	slog.Info("trigger started",
		"tenantId", event.TenantID,
	)

	// ── Step 1: load tenant ───────────────────────────────────────────────
	tenant, err := db.GetTenant(ctx, event.TenantID)
	if err != nil {
		slog.Error("db: failed to load tenant",
			"tenantId", event.TenantID,
			"error", err,
		)
		return fmt.Errorf("db get tenant: %w", err)
	}
	if tenant == nil {
		slog.Error("db: tenant not found", "tenantId", event.TenantID)
		return fmt.Errorf("tenant %q not found", event.TenantID)
	}
	slog.Info("db: tenant loaded",
		"tenantId", tenant.TenantID,
		"name", tenant.Name,
		"hotelCode", tenant.HotelCode,
		"timezone", tenant.Timezone,
		"scheduledAt", tenant.RunTime,
		"lat", tenant.Latitude,
		"lon", tenant.Longitude,
	)

	// ── Step 2: fetch weather (substitute for OHIP API) ───────────────────
	weather, err := fetchWeather(tenant.Latitude, tenant.Longitude)
	if err != nil {
		slog.Error("api: weather fetch failed",
			"tenantId", tenant.TenantID,
			"hotelCode", tenant.HotelCode,
			"error", err,
		)
		return fmt.Errorf("fetch weather for %s: %w", tenant.HotelCode, err)
	}
	slog.Info("api: weather fetched",
		"tenantId", tenant.TenantID,
		"hotelCode", tenant.HotelCode,
		"localTime", weather.Current.Time,
		"temperature", fmt.Sprintf("%.1f°C", weather.Current.Temperature),
		"windSpeed", fmt.Sprintf("%.1f km/h", weather.Current.WindSpeed),
		"weatherCode", weather.Current.WeatherCode,
	)

	// ── Step 3: build payload and push to SQS ────────────────────────────
	payload := sqsPayload{
		TenantID:    tenant.TenantID,
		HotelCode:   tenant.HotelCode,
		ExecutedAt:  time.Now().UTC().Format(time.RFC3339),
		WeatherData: *weather,
	}
	body, _ := json.Marshal(payload)

	result, err := sqsClient.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(os.Getenv("SQS_QUEUE_URL")),
		MessageBody: aws.String(string(body)),
		MessageAttributes: map[string]sqstypes.MessageAttributeValue{
			"tenantId": {
				DataType:    aws.String("String"),
				StringValue: aws.String(tenant.TenantID),
			},
		},
	})
	if err != nil {
		slog.Error("sqs: send failed",
			"tenantId", tenant.TenantID,
			"error", err,
		)
		return fmt.Errorf("sqs send: %w", err)
	}

	slog.Info("trigger complete",
		"tenantId", tenant.TenantID,
		"hotelCode", tenant.HotelCode,
		"sqsMessageId", aws.ToString(result.MessageId),
		"durationMs", time.Since(start).Milliseconds(),
	)
	return nil
}

func main() {
	lambda.Start(handler)
}
