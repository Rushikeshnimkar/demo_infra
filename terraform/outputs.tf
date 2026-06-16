output "api_endpoint" {
  description = "HTTP API Gateway base URL — use this as API_URL when seeding"
  value       = aws_apigatewayv2_api.pms.api_endpoint
}

output "rds_endpoint" {
  description = "RDS hostname"
  value       = aws_db_instance.pms.address
}

output "sqs_queue_url" {
  description = "SQS queue URL"
  value       = aws_sqs_queue.pms.url
}

output "dlq_url" {
  description = "Dead-letter queue URL"
  value       = aws_sqs_queue.dlq.url
}

output "scheduler_group_name" {
  description = "EventBridge Scheduler Group name"
  value       = aws_scheduler_schedule_group.pms.name
}

output "trigger_lambda_arn" {
  description = "Trigger Lambda ARN (for manual testing)"
  value       = aws_lambda_function.trigger.arn
}

output "lambda_bucket" {
  description = "S3 bucket holding Lambda deployment zips"
  value       = aws_s3_bucket.lambda_artifacts.bucket
}
