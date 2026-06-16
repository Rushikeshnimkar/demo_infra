resource "aws_sqs_queue" "dlq" {
  name                      = "${var.project}-dlq"
  message_retention_seconds = 1209600 # 14 days
  tags                      = { Name = "${var.project}-dlq" }
}

resource "aws_sqs_queue" "pms" {
  name                       = "${var.project}-queue"
  message_retention_seconds  = 86400 # 1 day
  visibility_timeout_seconds = 300

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq.arn
    maxReceiveCount     = 3
  })

  tags = { Name = "${var.project}-queue" }
}
