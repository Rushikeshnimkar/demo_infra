resource "aws_cloudwatch_log_group" "api_lambda" {
  name              = "/aws/lambda/${var.project}-api"
  retention_in_days = 7
}

resource "aws_cloudwatch_log_group" "trigger_lambda" {
  name              = "/aws/lambda/${var.project}-trigger"
  retention_in_days = 7
}

locals {
  db_dsn = "postgresql://${var.db_username}:${var.db_password}@${aws_db_instance.pms.address}:5432/${var.db_name}?sslmode=require"
}

# Zips are uploaded to S3 via `make upload` before terraform apply.
# Using S3 avoids the large base64-encoded HTTP body that causes the
# Lambda direct-upload API to hang on files over ~5 MB.

resource "aws_lambda_function" "api" {
  function_name    = "${var.project}-api"
  role             = aws_iam_role.api_lambda.arn
  runtime          = "provided.al2023"
  handler          = "bootstrap"
  s3_bucket        = aws_s3_bucket.lambda_artifacts.bucket
  s3_key           = "api.zip"
  source_code_hash = filebase64sha256("/tmp/pms-build/api/api.zip")

  timeout     = 30
  memory_size = 256

  environment {
    variables = {
      DB_DSN               = local.db_dsn
      TRIGGER_LAMBDA_ARN   = aws_lambda_function.trigger.arn
      SCHEDULER_ROLE_ARN   = aws_iam_role.scheduler_execution.arn
      SCHEDULER_GROUP_NAME = aws_scheduler_schedule_group.pms.name
    }
  }

  depends_on = [aws_cloudwatch_log_group.api_lambda]
}

resource "aws_lambda_function" "trigger" {
  function_name    = "${var.project}-trigger"
  role             = aws_iam_role.trigger_lambda.arn
  runtime          = "provided.al2023"
  handler          = "bootstrap"
  s3_bucket        = aws_s3_bucket.lambda_artifacts.bucket
  s3_key           = "trigger.zip"
  source_code_hash = filebase64sha256("/tmp/pms-build/trigger/trigger.zip")

  timeout     = 60
  memory_size = 256

  environment {
    variables = {
      DB_DSN        = local.db_dsn
      SQS_QUEUE_URL = aws_sqs_queue.pms.url
    }
  }

  depends_on = [aws_cloudwatch_log_group.trigger_lambda]
}

resource "aws_lambda_permission" "scheduler_invoke" {
  statement_id  = "AllowEventBridgeScheduler"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.trigger.function_name
  principal     = "scheduler.amazonaws.com"
  source_arn    = "arn:aws:scheduler:${var.aws_region}:${data.aws_caller_identity.current.account_id}:schedule/${aws_scheduler_schedule_group.pms.name}/*"
}
