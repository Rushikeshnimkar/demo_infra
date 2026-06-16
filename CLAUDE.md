# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project does

Multi-tenant PMS scheduler: each tenant stores a daily run-time + timezone in RDS. A backend API creates/updates one **EventBridge Scheduler** per tenant (cron expression + `ScheduleExpressionTimezone`). At the scheduled time, EventBridge invokes the **Trigger Lambda**, which calls the Open-Meteo weather API (substitute for OHIP) and pushes a JSON payload to SQS. RDS is the source of truth; schedulers are derived resources and can be recreated from the DB at any time.

## Build commands

> Windows users: run `build.ps1` instead of `make`.

```bash
# Build both Lambda zips (must run before terraform apply)
make build

# Build individual
make build-api
make build-trigger

# Deploy everything
make deploy          # runs make build, then terraform apply -auto-approve

# Seed 10 sample tenants
make seed API_URL=https://<id>.execute-api.us-east-1.amazonaws.com
```

PowerShell equivalent:
```powershell
.\build.ps1               # both
.\build.ps1 -ApiOnly      # api only
.\build.ps1 -TriggerOnly  # trigger only
```

## Terraform workflow

```bash
cd terraform
cp terraform.tfvars.example terraform.tfvars   # fill in db_password
terraform init
terraform plan
terraform apply
terraform output api_endpoint   # use this as API_URL
```

The zip files at `bin/api/api.zip` and `bin/trigger/trigger.zip` must exist before `terraform plan` because `filebase64sha256` is evaluated at plan time.

## API endpoints

All routes are handled by the API Lambda behind HTTP API Gateway v2.

| Method | Path | Description |
|--------|------|-------------|
| `GET`  | `/tenants` | List all tenants |
| `POST` | `/tenants` | Create tenant + EventBridge Scheduler |
| `GET`  | `/tenants/{id}` | Get tenant |
| `PUT`  | `/tenants/{id}/schedule` | Update timezone/runTime/enabled + reschedule |
| `DELETE` | `/tenants/{id}` | Delete tenant + remove scheduler |

`PUT /tenants/{id}/schedule` body:
```json
{ "timezone": "Asia/Kolkata", "runTime": "08:00", "enabled": true }
```

## Key design decisions

**Scheduler upsert pattern** (`internal/scheduler/scheduler.go`): `UpdateSchedule` is tried first; only if `ResourceNotFoundException` is returned does `CreateSchedule` run. This avoids a redundant `GetSchedule` round-trip on the hot path.

**Cron expression** (`cronExpression` func): `"HH:MM"` → `"cron(MM HH * * ? *)"`. The `ScheduleExpressionTimezone` field on the EventBridge Scheduler resource handles DST automatically — no application-level timezone math needed.

**DB migration**: `internal/db/db.go:Migrate()` runs `CREATE TABLE IF NOT EXISTS` in the API Lambda's `init()` block. It is idempotent and only executes on cold starts.

**Open-Meteo** is the open-source API used by the Trigger Lambda in place of OHIP. No API key required. Each tenant record stores `latitude`/`longitude` for the weather lookup.

## Environment variables

| Lambda | Variable | Description |
|--------|----------|-------------|
| api | `DB_DSN` | PostgreSQL connection string |
| api | `TRIGGER_LAMBDA_ARN` | ARN of the trigger Lambda (passed to scheduler target) |
| api | `SCHEDULER_ROLE_ARN` | IAM role ARN that EventBridge assumes to invoke the trigger Lambda |
| api | `SCHEDULER_GROUP_NAME` | EventBridge Scheduler Group name (`pms-schedulers`) |
| trigger | `DB_DSN` | PostgreSQL connection string |
| trigger | `SQS_QUEUE_URL` | SQS queue URL for downstream payloads |

All are injected by Terraform via `aws_lambda_function.environment`.

## Project structure

```
internal/models/   shared structs (Tenant, request types)
internal/db/       PostgreSQL CRUD using pgx/v5/stdlib driver
internal/scheduler/ EventBridge Scheduler create/update/disable/delete
lambdas/api/       HTTP API Lambda — routing, DB write, scheduler upsert
lambdas/trigger/   EventBridge target Lambda — fetch weather, push to SQS
scripts/seed/      one-shot CLI to POST 10 sample tenants to the live API
terraform/         all infra except individual tenant schedulers
```

## Terraform ownership

Terraform manages: Scheduler Group, both Lambdas, SQS + DLQ, IAM roles, API Gateway, CloudWatch log groups, RDS.

Terraform does **not** manage individual tenant schedulers (`tenant-001`, `tenant-002`, …). Those are created dynamically by the API Lambda at runtime.
