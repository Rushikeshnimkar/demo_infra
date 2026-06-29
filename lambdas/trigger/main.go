package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

type sqsPayload struct {
	TenantID    string `json:"tenantId"`
	PMSProvider string `json:"pmsProvider"`
	ExecutedAt  string `json:"executedAt"`
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

func handler(ctx context.Context, event TriggerEvent) error {
	start := time.Now()
	slog.Info("trigger started", "tenantId", event.TenantID)

	tenant, err := db.GetTenant(ctx, event.TenantID)
	if err != nil {
		return fmt.Errorf("db get tenant: %w", err)
	}
	if tenant == nil {
		return fmt.Errorf("tenant %q not found", event.TenantID)
	}
	slog.Info("db: tenant loaded",
		"tenantId", tenant.TenantID,
		"pmsProvider", tenant.PMSProvider,
	)

	payload := sqsPayload{
		TenantID:    tenant.TenantID,
		PMSProvider: tenant.PMSProvider,
		ExecutedAt:  time.Now().UTC().Format(time.RFC3339),
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
		return fmt.Errorf("sqs send: %w", err)
	}

	slog.Info("trigger complete",
		"tenantId", tenant.TenantID,
		"sqsMessageId", aws.ToString(result.MessageId),
		"durationMs", time.Since(start).Milliseconds(),
	)
	return nil
}

func main() {
	lambda.Start(handler)
}
