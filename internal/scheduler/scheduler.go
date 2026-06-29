package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awssched "github.com/aws/aws-sdk-go-v2/service/scheduler"
	"github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"multi-tenant-scheduler/internal/models"
)

var client *awssched.Client

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		panic(fmt.Sprintf("load AWS config: %v", err))
	}
	client = awssched.NewFromConfig(cfg)
}

// schedulerName derives the EventBridge scheduler name from the tenant ID.
// Keeping it deterministic means we never need to store the name anywhere.
func schedulerName(tenantID string) string {
	return tenantID
}

func env(key string) *string {
	return aws.String(os.Getenv(key))
}

// Get fetches the live schedule for a tenant directly from EventBridge.
// Returns nil if the scheduler does not exist.
func Get(ctx context.Context, tenantID string) (*models.ScheduleInfo, error) {
	got, err := client.GetSchedule(ctx, &awssched.GetScheduleInput{
		GroupName: env("SCHEDULER_GROUP_NAME"),
		Name:      aws.String(schedulerName(tenantID)),
	})
	if err != nil {
		var notFound *types.ResourceNotFoundException
		if errors.As(err, &notFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get scheduler: %w", err)
	}
	state := "ENABLED"
	if got.State == types.ScheduleStateDisabled {
		state = "DISABLED"
	}
	return &models.ScheduleInfo{
		Expression: aws.ToString(got.ScheduleExpression),
		Timezone:   aws.ToString(got.ScheduleExpressionTimezone),
		State:      state,
	}, nil
}

// Upsert creates or updates the EventBridge Scheduler for a tenant.
// Tries UpdateSchedule first; falls back to CreateSchedule on ResourceNotFoundException.
func Upsert(ctx context.Context, tenantID, expression, timezone string, enabled bool) error {
	state := types.ScheduleStateEnabled
	if !enabled {
		state = types.ScheduleStateDisabled
	}
	payload := fmt.Sprintf(`{"tenantId":%q}`, tenantID)

	input := awssched.UpdateScheduleInput{
		GroupName:                  env("SCHEDULER_GROUP_NAME"),
		Name:                       aws.String(schedulerName(tenantID)),
		ScheduleExpression:         aws.String(expression),
		ScheduleExpressionTimezone: aws.String(timezone),
		State:                      state,
		Target: &types.Target{
			Arn:     env("TRIGGER_LAMBDA_ARN"),
			RoleArn: env("SCHEDULER_ROLE_ARN"),
			Input:   aws.String(payload),
		},
		FlexibleTimeWindow: &types.FlexibleTimeWindow{
			Mode: types.FlexibleTimeWindowModeOff,
		},
	}

	_, err := client.UpdateSchedule(ctx, &input)
	if err == nil {
		return nil
	}
	var notFound *types.ResourceNotFoundException
	if !errors.As(err, &notFound) {
		return fmt.Errorf("update scheduler: %w", err)
	}

	_, err = client.CreateSchedule(ctx, &awssched.CreateScheduleInput{
		GroupName:                  input.GroupName,
		Name:                       input.Name,
		ScheduleExpression:         input.ScheduleExpression,
		ScheduleExpressionTimezone: input.ScheduleExpressionTimezone,
		State:                      input.State,
		Target:                     input.Target,
		FlexibleTimeWindow:         input.FlexibleTimeWindow,
	})
	if err != nil {
		return fmt.Errorf("create scheduler: %w", err)
	}
	return nil
}

// Delete removes the scheduler for a tenant. Safe to call when already deleted.
func Delete(ctx context.Context, tenantID string) error {
	_, err := client.DeleteSchedule(ctx, &awssched.DeleteScheduleInput{
		GroupName: env("SCHEDULER_GROUP_NAME"),
		Name:      aws.String(schedulerName(tenantID)),
	})
	var notFound *types.ResourceNotFoundException
	if errors.As(err, &notFound) {
		return nil
	}
	return err
}
