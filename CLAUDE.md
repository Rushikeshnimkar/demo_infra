# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project does

Multi-tenant PMS scheduler: each tenant has a PMS provider and a JSONB config (for non-schedule metadata) stored in RDS. **Schedule details (cron expression, timezone, enabled state) live only in EventBridge** — they are never stored in the database. The API creates/updates one EventBridge Scheduler per tenant and reads schedule info back from EventBridge on demand. At the scheduled time, EventBridge invokes the Trigger Lambda, which loads the tenant from DB and pushes a JSON payload to SQS.

**EventBridge is the source of truth for schedules. RDS is the source of truth for tenant identity and metadata.**

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
| `GET`  | `/tenants` | List tenants with config (no schedule details) |
| `POST` | `/tenants` | Create tenant + config in DB, create EventBridge Scheduler |
| `GET`  | `/tenants/{id}` | Get tenant + config from DB + live schedule from EventBridge |
| `PUT`  | `/tenants/{id}/schedule` | Update EventBridge Scheduler only — no DB write |
| `PUT`  | `/tenants/{id}/config` | Update tenant_config JSONB in DB only — no EventBridge call |
| `DELETE` | `/tenants/{id}` | Delete tenant from DB + delete scheduler |

## Database schema

One table — `config` JSONB column holds arbitrary non-schedule metadata alongside tenant identity.

```sql
CREATE TABLE tenant (
    tenant_id    VARCHAR(50)  PRIMARY KEY,
    pms_provider VARCHAR(100) NOT NULL,
    config       JSONB        NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
```

`config` JSONB holds arbitrary tenant metadata — hotel name, codes, etc. **It does not contain schedule fields** (expression, timezone, cron, enabled). Those live only in EventBridge.

## Key design decisions

**Schedule data lives only in EventBridge**: `PUT /tenants/{id}/schedule` calls `UpdateSchedule` / `CreateSchedule` and does not touch the database. `GET /tenants/{id}` calls `GetSchedule` to read live schedule info. This removes any risk of DB and EventBridge drifting out of sync on schedule state.

**Config JSONB holds non-schedule metadata only**: `PUT /tenants/{id}/config` writes to `tenant_config` only — it never touches EventBridge. This gives each tenant a flexible store for hotel names, codes, and any other PMS-specific data.

**Scheduler name = tenantID**: The EventBridge scheduler name is always derived as `tenantID` (e.g. tenant `"tenant-001"` → scheduler `"tenant-001"`). Deterministic — no need to store the name anywhere.

**Scheduler upsert pattern** (`internal/scheduler/scheduler.go`): `UpdateSchedule` is tried first; only if `ResourceNotFoundException` is returned does `CreateSchedule` run. Avoids a redundant `GetSchedule` round-trip on the hot path.

**Schedule first, DB second on create**: On `POST /tenants`, the EventBridge scheduler is created before the DB transaction. If the DB transaction fails, the scheduler is deleted as a best-effort rollback.

**Terraform-managed initial schedulers use `ignore_changes = all`**: Pre-defined schedulers in `terraform/scheduler.tf` are created once by Terraform. After that, `lifecycle { ignore_changes = all }` prevents any `terraform apply` from modifying them — the API Lambda owns their state from that point forward.

**DB migration** (`internal/db/db.go:Migrate()`): runs in `init()` on cold start. Drops `tenant_config` if it exists (legacy cleanup), then creates the single `tenant` table with `IF NOT EXISTS` — idempotent.

**One provider per tenant**: `pms_provider` is a plain VARCHAR on the `tenant` row.

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
internal/models/    Tenant, ScheduleInfo, TenantWithSchedule, request types
internal/db/        PostgreSQL CRUD (pgx/v5/stdlib); single tenant table with config JSONB
internal/scheduler/ EventBridge Scheduler get/upsert/delete
lambdas/api/        HTTP API Lambda — routing, EventBridge calls, DB writes
lambdas/trigger/    EventBridge target Lambda — load tenant, push to SQS
scripts/seed/       one-shot CLI to POST 10 sample tenants to the live API
terraform/          all infra + pre-defined tenant schedulers (ignore_changes)
```

## Terraform ownership

Terraform manages: Scheduler Group, pre-defined tenant schedulers (with `ignore_changes = all`), both Lambdas, SQS + DLQ, IAM roles, API Gateway, CloudWatch log groups, RDS.

Pre-defined schedulers in `terraform/scheduler.tf` are created once on first `terraform apply` and then **never modified by Terraform again** — add `lifecycle { ignore_changes = all }` to every `aws_scheduler_schedule` resource. Dynamically created tenants (via `POST /tenants`) are managed entirely by the API Lambda.

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

Returns all tenants with their config from DB. Does **not** include schedule details — call `GET /tenants/{id}` for those.

```bash
curl $BASE_URL/tenants
```

**200 OK**
```json
[
  {
    "tenantId":    "tenant-001",
    "pmsProvider": "Opera",
    "config":      { "name": "Grand Hotel", "hotelCode": "GH001" },
    "createdAt":   "2026-06-29T10:00:00Z"
  }
]
```

Returns `[]` when no tenants exist.

---

### POST /tenants

Creates the EventBridge Scheduler first, then inserts tenant + config into DB in a transaction. Rolls back the scheduler on DB failure.

```bash
curl -X POST $BASE_URL/tenants \
  -H "Content-Type: application/json" \
  -d '{
    "tenantId":    "tenant-001",
    "pmsProvider": "Opera",
    "config":      { "name": "Grand Hotel", "hotelCode": "GH001" },
    "expression":  "cron(0 7 * * ? *)",
    "timezone":    "Asia/Kolkata",
    "enabled":     true
  }'
```

**Request fields**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `tenantId` | string | yes | Primary key, max 50 chars. Also used as the EventBridge scheduler name. |
| `pmsProvider` | string | yes | PMS name — e.g. `"Opera"`, `"Mews"`, `"Apaleo"` |
| `config` | object | no | Arbitrary JSON metadata for non-schedule data. Stored in tenant_config. |
| `expression` | string | yes | Full EventBridge expression — e.g. `"cron(0 7 * * ? *)"` or `"rate(1 hour)"` |
| `timezone` | string | yes | IANA timezone — e.g. `"Asia/Kolkata"`, `"America/New_York"` |
| `enabled` | bool | no | `true` to activate immediately. Defaults to `false` if omitted. |

**Cron expression format**

| Run time | Expression |
|----------|-----------|
| Every day at 07:00 | `cron(0 7 * * ? *)` |
| Every day at 08:30 | `cron(30 8 * * ? *)` |
| Every day at 18:00 | `cron(0 18 * * ? *)` |
| Every hour | `rate(1 hour)` |
| Every 30 minutes | `rate(30 minutes)` |

**201 Created**
```json
{
  "tenantId":    "tenant-001",
  "pmsProvider": "Opera",
  "config":      { "name": "Grand Hotel", "hotelCode": "GH001" },
  "schedule": {
    "expression": "cron(0 7 * * ? *)",
    "timezone":   "Asia/Kolkata",
    "state":      "ENABLED"
  }
}
```

**400 Bad Request**
```json
{ "error": "tenantId is required" }
{ "error": "pmsProvider is required" }
{ "error": "expression is required" }
{ "error": "timezone is required" }
```

**500 Internal Server Error**
```json
{ "error": "scheduler: ..." }
```

---

### GET /tenants/{id}

Returns tenant + config from DB and live schedule from EventBridge.

```bash
curl $BASE_URL/tenants/tenant-001
```

**200 OK**
```json
{
  "tenantId":    "tenant-001",
  "pmsProvider": "Opera",
  "config":      { "name": "Grand Hotel", "hotelCode": "GH001" },
  "schedule": {
    "expression": "cron(0 7 * * ? *)",
    "timezone":   "Asia/Kolkata",
    "state":      "ENABLED"
  },
  "createdAt": "2026-06-29T10:00:00Z"
}
```

`schedule` is `null` if no EventBridge scheduler exists for this tenant.

**404 Not Found**
```json
{ "error": "tenant not found" }
```

---

### PUT /tenants/{id}/schedule

Updates the EventBridge Scheduler **only** — no database write. DB is checked first to confirm the tenant exists.

```bash
curl -X PUT $BASE_URL/tenants/tenant-001/schedule \
  -H "Content-Type: application/json" \
  -d '{
    "expression": "cron(30 8 * * ? *)",
    "timezone":   "Asia/Kolkata",
    "enabled":    true
  }'
```

Setting `enabled: false` disables the scheduler without deleting it.

**200 OK** `{ "status": "updated" }`

**400 Bad Request** `{ "error": "expression is required" }` / `"timezone is required"`

**404 Not Found** `{ "error": "tenant not found" }`

**500 Internal Server Error** `{ "error": "scheduler: ..." }`

---

### PUT /tenants/{id}/config

Replaces the tenant_config JSONB. **No EventBridge call.** DB is checked first to confirm the tenant exists.

Do **not** include schedule fields (`expression`, `timezone`, `enabled`) in the config object.

```bash
curl -X PUT $BASE_URL/tenants/tenant-001/config \
  -H "Content-Type: application/json" \
  -d '{
    "config": { "name": "Grand Hotel Updated", "hotelCode": "GH001", "region": "APAC" }
  }'
```

**200 OK** `{ "status": "updated" }`

**400 Bad Request** `{ "error": "config is required" }`

**404 Not Found** `{ "error": "tenant not found" }`

**500 Internal Server Error** `{ "error": "..." }`

---

### DELETE /tenants/{id}

Deletes the tenant row (cascades to tenant_config), then deletes the EventBridge Scheduler.

```bash
curl -X DELETE $BASE_URL/tenants/tenant-001
```

**200 OK** `{ "status": "deleted" }`

**404 Not Found** `{ "error": "tenant not found" }`

**500 Internal Server Error** `{ "error": "delete scheduler: ..." }`
