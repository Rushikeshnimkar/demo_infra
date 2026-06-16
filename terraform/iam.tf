locals {
  lambda_assume = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
    }]
  })
}

# ── API Lambda ──────────────────────────────────────────────────────────────

resource "aws_iam_role" "api_lambda" {
  name               = "${var.project}-api-lambda-role"
  assume_role_policy = local.lambda_assume
}

resource "aws_iam_role_policy" "api_lambda" {
  name = "${var.project}-api-lambda-policy"
  role = aws_iam_role.api_lambda.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "Logs"
        Effect   = "Allow"
        Action   = ["logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"]
        Resource = "*"
      },
      {
        Sid    = "Scheduler"
        Effect = "Allow"
        Action = [
          "scheduler:CreateSchedule",
          "scheduler:UpdateSchedule",
          "scheduler:DeleteSchedule",
          "scheduler:GetSchedule",
          "scheduler:ListSchedules",
        ]
        Resource = "*"
      },
      {
        # API Lambda must pass the scheduler-execution role when creating schedules
        Sid      = "PassRole"
        Effect   = "Allow"
        Action   = "iam:PassRole"
        Resource = aws_iam_role.scheduler_execution.arn
      },
    ]
  })
}

# ── Trigger Lambda ──────────────────────────────────────────────────────────

resource "aws_iam_role" "trigger_lambda" {
  name               = "${var.project}-trigger-lambda-role"
  assume_role_policy = local.lambda_assume
}

resource "aws_iam_role_policy" "trigger_lambda" {
  name = "${var.project}-trigger-lambda-policy"
  role = aws_iam_role.trigger_lambda.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "Logs"
        Effect   = "Allow"
        Action   = ["logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"]
        Resource = "*"
      },
      {
        Sid      = "SQS"
        Effect   = "Allow"
        Action   = ["sqs:SendMessage", "sqs:GetQueueAttributes"]
        Resource = aws_sqs_queue.pms.arn
      },
    ]
  })
}

# ── EventBridge Scheduler execution role ────────────────────────────────────
# EventBridge assumes this role when invoking the trigger Lambda.

resource "aws_iam_role" "scheduler_execution" {
  name = "${var.project}-scheduler-execution-role"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "scheduler.amazonaws.com" }
    }]
  })
}

resource "aws_iam_role_policy" "scheduler_execution" {
  name = "${var.project}-scheduler-execution-policy"
  role = aws_iam_role.scheduler_execution.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "lambda:InvokeFunction"
      Resource = aws_lambda_function.trigger.arn
    }]
  })
}
