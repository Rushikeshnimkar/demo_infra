variable "aws_region" {
  description = "AWS region to deploy into"
  type        = string
  default     = "us-east-1"
}

variable "project" {
  description = "Short name used as a prefix for all resources"
  type        = string
  default     = "pms"
}

variable "db_username" {
  description = "RDS master username"
  type        = string
  default     = "pmsadmin"
  sensitive   = true
}

variable "db_password" {
  description = "RDS master password (min 8 chars)"
  type        = string
  sensitive   = true
}

variable "db_name" {
  description = "PostgreSQL database name"
  type        = string
  default     = "pmsdb"
}
