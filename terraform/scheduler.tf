# Terraform only owns the group.
# Individual tenant schedulers are created/updated/deleted dynamically
# by the API Lambda using the AWS SDK — never via Terraform.
resource "aws_scheduler_schedule_group" "pms" {
  name = "${var.project}-schedulers"
  tags = { Name = "${var.project}-schedulers" }
}
