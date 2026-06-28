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
)

var client *awssched.Client

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		panic(fmt.Sprintf("load AWS config: %v", err))
	}
	client = awssched.NewFromConfig(cfg)
}

func env(key string) *string {
	return aws.String(os.Getenv(key))
}

// Upsert creates or updates the EventBridge Scheduler for a tenant.
// name is the scheduler name (from config.Name), cronExpr is passed through directly.
// Tries Update first and falls back to Create only on ResourceNotFoundException.
func Upsert(ctx context.Context, name, timezone, cronExpr, tenantID string, enabled bool) error {
	state := types.ScheduleStateEnabled
	if !enabled {
		state = types.ScheduleStateDisabled
	}
	payload := fmt.Sprintf(`{"tenantId":%q}`, tenantID)

	input := awssched.UpdateScheduleInput{
		GroupName:                  env("SCHEDULER_GROUP_NAME"),
		Name:                       aws.String(name),
		ScheduleExpression:         aws.String(cronExpr),
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

// Delete removes the scheduler by name. Safe to call when already deleted.
func Delete(ctx context.Context, name string) error {
	_, err := client.DeleteSchedule(ctx, &awssched.DeleteScheduleInput{
		GroupName: env("SCHEDULER_GROUP_NAME"),
		Name:      aws.String(name),
	})
	var notFound *types.ResourceNotFoundException
	if errors.As(err, &notFound) {
		return nil
	}
	return err
}
