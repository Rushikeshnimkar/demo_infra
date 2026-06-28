# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project does

Multi-tenant PMS scheduler: each tenant has a PMS provider and a schedule config stored in RDS. A backend API creates/updates one **EventBridge Scheduler** per tenant using the cron expression and timezone from the config. At the scheduled time, EventBridge invokes the **Trigger Lambda**, which reads the tenant record from DB and pushes a JSON payload to SQS. RDS is the source of truth; schedulers are derived resources and can be recreated from the DB at any time.

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
| `GET`  | `/health` | DB liveness check |
| `GET`  | `/tenants` | List all tenants with config |
| `POST` | `/tenants` | Create tenant + EventBridge Scheduler |
| `GET`  | `/tenants/{id}` | Get tenant with config |
| `PUT`  | `/tenants/{id}/config` | Replace config JSON + reschedule |
| `DELETE` | `/tenants/{id}` | Delete tenant + remove scheduler |

> **Note:** The old flat fields (`name`, `scheduleType`, `runTime`, `rateMinutes`, `latitude`, `longitude`, `hotelCode`) are gone. All schedule details now live inside the nested `config` object. See the **API reference** section below for full curl examples.

## Database schema

Two tables — `tenant` is the parent, `tenant_config` holds the JSONB schedule config.

```sql
CREATE TABLE tenant (
    tenant_id    VARCHAR(50)  PRIMARY KEY,
    pms_provider VARCHAR(100) NOT NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE tenant_config (
    tenant_id  VARCHAR(50) PRIMARY KEY REFERENCES tenant(tenant_id) ON DELETE CASCADE,
    config     JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

`config` JSONB shape: `{ name, timezone, cron, enabled }`.

## Key design decisions

**Scheduler upsert pattern** (`internal/scheduler/scheduler.go`): `UpdateSchedule` is tried first; only if `ResourceNotFoundException` is returned does `CreateSchedule` run. This avoids a redundant `GetSchedule` round-trip on the hot path.

**Cron passed through directly**: callers supply the full EventBridge cron expression (e.g. `cron(0 7 * * ? *)`). There is no server-side conversion from `HH:MM`. `ScheduleExpressionTimezone` handles DST automatically.

**Schedule first, DB second**: on create and update, the EventBridge scheduler is created/updated before the DB write. If the DB write fails, the scheduler is deleted as a best-effort rollback.

**DB migration** (`internal/db/db.go:Migrate()`): runs in `init()` on cold start. Detects old schema by checking for the `tenant` table. If absent, drops the legacy `tenant_config` table and creates both new tables. Idempotent on subsequent cold starts.

**One provider per tenant**: `pms_provider` is a plain VARCHAR on the `tenant` row. There is no separate provider table.

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
internal/models/    ScheduleConfig, TenantWithConfig, request types
internal/db/        PostgreSQL CRUD (pgx/v5/stdlib); tenant + tenant_config tables
internal/scheduler/ EventBridge Scheduler upsert/delete
lambdas/api/        HTTP API Lambda — routing, scheduler upsert, DB write
lambdas/trigger/    EventBridge target Lambda — load tenant, push to SQS
scripts/seed/       one-shot CLI to POST 10 sample tenants to the live API
terraform/          all infra except individual tenant schedulers
```

## API reference

Replace `$BASE_URL` with the value of `terraform output api_endpoint`.

---

### GET /health

```bash
curl $BASE_URL/health
```

**200 OK**
```json
{ "status": "ok" }
```

**503 Service Unavailable**
```json
{ "error": "database unavailable" }
```

---

### GET /tenants

```bash
curl $BASE_URL/tenants
```

**200 OK**
```json
[
  {
    "tenantId": "tenant-001",
    "pmsProvider": "Opera",
    "config": {
      "name": "tenant-001-run",
      "timezone": "Asia/Kolkata",
      "cron": "cron(0 7 * * ? *)",
      "enabled": true
    },
    "createdAt": "2026-06-28T10:00:00Z",
    "updatedAt": "2026-06-28T10:00:00Z"
  }
]
```

Returns `[]` when no tenants exist.

---

### POST /tenants

Creates the tenant, its config, and an EventBridge Scheduler. Scheduler is created first; the DB write is rolled back (scheduler deleted) on failure.

```bash
curl -X POST $BASE_URL/tenants \
  -H "Content-Type: application/json" \
  -d '{
    "tenantId":    "tenant-001",
    "pmsProvider": "Opera",
    "config": {
      "name":     "tenant-001-run",
      "timezone": "Asia/Kolkata",
      "cron":     "cron(0 7 * * ? *)",
      "enabled":  true
    }
  }'
```

**Request fields**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `tenantId` | string | yes | Primary key, max 50 chars |
| `pmsProvider` | string | yes | PMS name — e.g. `"Opera"`, `"Mews"`, `"Apaleo"` |
| `config.name` | string | yes | EventBridge scheduler name — must be unique within the scheduler group |
| `config.timezone` | string | yes | IANA timezone — e.g. `"Asia/Kolkata"`, `"America/New_York"`, `"Europe/London"` |
| `config.cron` | string | yes | Full EventBridge cron expression (see examples below) |
| `config.enabled` | bool | no | `true` to activate the scheduler, `false` to create it disabled. Defaults to `false` if omitted |

**Cron expression format**

EventBridge uses a 6-field cron: `cron(minutes hours day-of-month month day-of-week year)`.
Use `*` for "every", `?` as a placeholder for day-of-month or day-of-week (one must be `?`).

| Run time | Cron expression |
|----------|----------------|
| Every day at 07:00 | `cron(0 7 * * ? *)` |
| Every day at 08:30 | `cron(30 8 * * ? *)` |
| Every day at 18:00 | `cron(0 18 * * ? *)` |
| Every hour | `rate(1 hour)` |
| Every 30 minutes | `rate(30 minutes)` |

The `config.timezone` field is applied by EventBridge automatically — no UTC conversion needed.

**201 Created**
```json
{
  "tenantId":    "tenant-001",
  "pmsProvider": "Opera",
  "config": {
    "name":     "tenant-001-run",
    "timezone": "Asia/Kolkata",
    "cron":     "cron(0 7 * * ? *)",
    "enabled":  true
  },
  "createdAt": "0001-01-01T00:00:00Z",
  "updatedAt": "0001-01-01T00:00:00Z"
}
```

**400 Bad Request** — missing required field
```json
{ "error": "tenantId is required" }
{ "error": "pmsProvider is required" }
{ "error": "config.name is required" }
{ "error": "config.timezone is required" }
{ "error": "config.cron is required" }
```

**500 Internal Server Error** — EventBridge or DB failure
```json
{ "error": "scheduler: ..." }
```

---

### GET /tenants/{id}

```bash
curl $BASE_URL/tenants/tenant-001
```

**200 OK** — same shape as a single element from `GET /tenants`.

**404 Not Found**
```json
{ "error": "tenant not found" }
```

---

### PUT /tenants/{id}/config

Replaces the config JSON and updates the EventBridge Scheduler. Scheduler is updated first. If `config.name` changes, the old scheduler is deleted after the new one is created.

```bash
curl -X PUT $BASE_URL/tenants/tenant-001/config \
  -H "Content-Type: application/json" \
  -d '{
    "config": {
      "name": "tenant-001-run",
      "timezone": "Asia/Kolkata",
      "cron": "cron(30 8 * * ? *)",
      "enabled": false
    }
  }'
```

Setting `enabled: false` disables the EventBridge scheduler without deleting it.

**200 OK**
```json
{ "status": "updated" }
```

**400 Bad Request** — same field errors as POST.

**404 Not Found**
```json
{ "error": "tenant not found" }
```

**500 Internal Server Error**
```json
{ "error": "scheduler: ..." }
```

---

### DELETE /tenants/{id}

Deletes the tenant row (cascades to tenant_config), then deletes the EventBridge Scheduler.

```bash
curl -X DELETE $BASE_URL/tenants/tenant-001
```

**200 OK**
```json
{ "status": "deleted" }
```

**404 Not Found**
```json
{ "error": "tenant not found" }
```

**500 Internal Server Error**
```json
{ "error": "delete scheduler: ..." }
```

---

## Terraform ownership

Terraform manages: Scheduler Group, both Lambdas, SQS + DLQ, IAM roles, API Gateway, CloudWatch log groups, RDS.

Terraform does **not** manage individual tenant schedulers. Those are created dynamically by the API Lambda at runtime and named after `config.name`.
