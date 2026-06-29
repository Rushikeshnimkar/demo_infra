resource "aws_scheduler_schedule_group" "pms" {
  name = "${var.project}-schedulers"
  tags = { Name = "${var.project}-schedulers" }
}

# Initial tenant schedulers are defined here so they are created on first
# `terraform apply`. After that, `lifecycle { ignore_changes = all }` prevents
# Terraform from ever modifying them — the API Lambda owns their state.
#
# To add a new pre-defined tenant: add a new resource block and run
# `terraform apply`. Terraform will create it once and then leave it alone.
# To remove a pre-defined tenant: delete the resource block AND call
# DELETE /tenants/{id} via the API (Terraform destroy will remove the scheduler,
# but the DB row must be cleaned up through the API).

locals {
  scheduler_defaults = {
    group_name   = aws_scheduler_schedule_group.pms.name
    trigger_arn  = aws_lambda_function.trigger.arn
    role_arn     = aws_iam_role.scheduler_execution.arn
  }
}

resource "aws_scheduler_schedule" "tenant_001" {
  name       = "tenant-001"
  group_name = local.scheduler_defaults.group_name

  schedule_expression          = "cron(0 7 * * ? *)"
  schedule_expression_timezone = "Asia/Kolkata"

  flexible_time_window { mode = "OFF" }

  target {
    arn      = local.scheduler_defaults.trigger_arn
    role_arn = local.scheduler_defaults.role_arn
    input    = jsonencode({ tenantId = "tenant-001" })
  }

  lifecycle { ignore_changes = all }
}

resource "aws_scheduler_schedule" "tenant_002" {
  name       = "tenant-002"
  group_name = local.scheduler_defaults.group_name

  schedule_expression          = "cron(0 9 * * ? *)"
  schedule_expression_timezone = "America/New_York"

  flexible_time_window { mode = "OFF" }

  target {
    arn      = local.scheduler_defaults.trigger_arn
    role_arn = local.scheduler_defaults.role_arn
    input    = jsonencode({ tenantId = "tenant-002" })
  }

  lifecycle { ignore_changes = all }
}

resource "aws_scheduler_schedule" "tenant_003" {
  name       = "tenant-003"
  group_name = local.scheduler_defaults.group_name

  schedule_expression          = "cron(0 18 * * ? *)"
  schedule_expression_timezone = "Europe/London"

  flexible_time_window { mode = "OFF" }

  target {
    arn      = local.scheduler_defaults.trigger_arn
    role_arn = local.scheduler_defaults.role_arn
    input    = jsonencode({ tenantId = "tenant-003" })
  }

  lifecycle { ignore_changes = all }
}
