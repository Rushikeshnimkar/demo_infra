package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awssched "github.com/aws/aws-sdk-go-v2/service/scheduler"
	"github.com/aws/aws-sdk-go-v2/service/scheduler/types"
)

var client *awssched.Client

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		panic(fmt.Sprintf("load AWS config: %v", err))
	}
	client = awssched.NewFromConfig(cfg)
}

func schedulerName(tenantID string) string {
	return "tenant-" + tenantID
}

// buildExpression returns the EventBridge schedule expression and the timezone to use.
// For rate schedules the timezone is always "UTC" (EventBridge ignores it for rate expressions).
func buildExpression(scheduleType, timezone, runTime string, rateMinutes int) (expr, tz string) {
	if scheduleType == "rate" {
		if rateMinutes >= 60 && rateMinutes%60 == 0 {
			h := rateMinutes / 60
			if h == 1 {
				return "rate(1 hour)", "UTC"
			}
			return fmt.Sprintf("rate(%d hours)", h), "UTC"
		}
		if rateMinutes == 1 {
			return "rate(1 minute)", "UTC"
		}
		return fmt.Sprintf("rate(%d minutes)", rateMinutes), "UTC"
	}
	parts := strings.SplitN(runTime, ":", 2)
	return fmt.Sprintf("cron(%s %s * * ? *)", parts[1], parts[0]), timezone
}

func env(key string) *string {
	return aws.String(os.Getenv(key))
}

// Upsert creates or updates the EventBridge Scheduler for a tenant.
// It tries Update first (fast path for existing schedules) and falls
// back to Create only when the resource does not yet exist.
func Upsert(ctx context.Context, tenantID, scheduleType, timezone, runTime string, rateMinutes int) error {
	name := schedulerName(tenantID)
	payload := fmt.Sprintf(`{"tenantId":%q}`, tenantID)
	expr, tz := buildExpression(scheduleType, timezone, runTime, rateMinutes)

	updateInput := &awssched.UpdateScheduleInput{
		GroupName:                  env("SCHEDULER_GROUP_NAME"),
		Name:                       aws.String(name),
		ScheduleExpression:         aws.String(expr),
		ScheduleExpressionTimezone: aws.String(tz),
		State:                      types.ScheduleStateEnabled,
		Target: &types.Target{
			Arn:     env("TRIGGER_LAMBDA_ARN"),
			RoleArn: env("SCHEDULER_ROLE_ARN"),
			Input:   aws.String(payload),
		},
		FlexibleTimeWindow: &types.FlexibleTimeWindow{
			Mode: types.FlexibleTimeWindowModeOff,
		},
	}

	_, err := client.UpdateSchedule(ctx, updateInput)
	if err == nil {
		return nil
	}

	var notFound *types.ResourceNotFoundException
	if !errors.As(err, &notFound) {
		return fmt.Errorf("update scheduler: %w", err)
	}

	_, err = client.CreateSchedule(ctx, &awssched.CreateScheduleInput{
		GroupName:                  env("SCHEDULER_GROUP_NAME"),
		Name:                       aws.String(name),
		ScheduleExpression:         aws.String(expr),
		ScheduleExpressionTimezone: aws.String(tz),
		State:                      types.ScheduleStateEnabled,
		Target: &types.Target{
			Arn:     env("TRIGGER_LAMBDA_ARN"),
			RoleArn: env("SCHEDULER_ROLE_ARN"),
			Input:   aws.String(payload),
		},
		FlexibleTimeWindow: &types.FlexibleTimeWindow{
			Mode: types.FlexibleTimeWindowModeOff,
		},
	})
	if err != nil {
		return fmt.Errorf("create scheduler: %w", err)
	}
	return nil
}

// Disable pauses an existing scheduler without deleting it.
func Disable(ctx context.Context, tenantID string) error {
	groupName := os.Getenv("SCHEDULER_GROUP_NAME")
	name := schedulerName(tenantID)

	got, err := client.GetSchedule(ctx, &awssched.GetScheduleInput{
		GroupName: aws.String(groupName),
		Name:      aws.String(name),
	})
	if err != nil {
		var notFound *types.ResourceNotFoundException
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("get scheduler: %w", err)
	}

	_, err = client.UpdateSchedule(ctx, &awssched.UpdateScheduleInput{
		GroupName:                  aws.String(groupName),
		Name:                       aws.String(name),
		ScheduleExpression:         got.ScheduleExpression,
		ScheduleExpressionTimezone: got.ScheduleExpressionTimezone,
		State:                      types.ScheduleStateDisabled,
		Target: &types.Target{
			Arn:     env("TRIGGER_LAMBDA_ARN"),
			RoleArn: env("SCHEDULER_ROLE_ARN"),
			Input:   got.Target.Input,
		},
		FlexibleTimeWindow: got.FlexibleTimeWindow,
	})
	return err
}

// Delete removes the scheduler for a tenant. It is safe to call when
// the scheduler has already been deleted (idempotent).
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
