# Multi-Tenant PMS Scheduler — Architecture & Usage Guide

## Table of Contents

1. [What This System Does](#1-what-this-system-does)
2. [The Core Problem It Solves](#2-the-core-problem-it-solves)
3. [System Architecture](#3-system-architecture)
4. [How Multi-Tenancy Works](#4-how-multi-tenancy-works)
5. [How Scheduling Works](#5-how-scheduling-works)
6. [Component Deep-Dives](#6-component-deep-dives)
7. [IAM & Security Model](#7-iam--security-model)
8. [Data Flow — Step by Step](#8-data-flow--step-by-step)
9. [API Reference](#9-api-reference)
10. [Deployment Guide](#10-deployment-guide)
11. [Testing & Verification](#11-testing--verification)
12. [Operational Topics](#12-operational-topics)
13. [Limitations & Trade-offs](#13-limitations--trade-offs)
14. [Pricing Estimate](#14-pricing-estimate)

---

## 1. What This System Does

This system schedules a recurring job for each PMS tenant. Each tenant has:

- A **PMS provider** (e.g. Opera, Mews, Apaleo) — stored in RDS
- A **config** (arbitrary non-schedule metadata: hotel name, codes, etc.) — stored as JSONB in RDS
- A **schedule** (cron expression, timezone, enabled state) — stored **only in EventBridge**, never in the database

At the scheduled time, AWS fires a Lambda function for that specific tenant. The Lambda reads the tenant record from RDS and pushes a JSON payload to an SQS queue for downstream consumers.

**EventBridge is the source of truth for schedules. RDS is the source of truth for tenant identity and metadata.**

---

## 2. The Core Problem It Solves

### The naive approach (what this system avoids)

```
Every 1 minute:
  SELECT * FROM tenant WHERE scheduled_time = NOW()
  FOR EACH matching tenant:
    do work
```

Problems: Lambda runs 1440×/day doing nothing, DST requires app-level math, hard to scale.

### This system's approach

One **EventBridge Scheduler per tenant** — fires exactly at the right local time, AWS handles DST automatically, no polling.

```
EventBridge fires at exactly 07:00 Asia/Kolkata
      ↓
Trigger Lambda receives {"tenantId": "tenant-001"}
      ↓
Lambda knows exactly who to run for — no polling, no scanning
```

---

## 3. System Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│  Client (curl / PMS UI)                                             │
└──────────────────────┬──────────────────────────────────────────────┘
                       │ HTTPS
                       ▼
┌─────────────────────────────────────────────────────────────────────┐
│  API Gateway (HTTP API v2)                                          │
│  POST   /tenants                                                    │
│  GET    /tenants  /tenants/{id}                                     │
│  PUT    /tenants/{id}/schedule   PUT /tenants/{id}/config           │
│  DELETE /tenants/{id}                                               │
└──────────────────────┬──────────────────────────────────────────────┘
                       │ Lambda Proxy
                       ▼
┌─────────────────────────────────────────────────────────────────────┐
│  API Lambda (Go)                                                    │
│  - DB: tenant identity (pms_provider) + config JSONB metadata       │
│  - EventBridge: schedule CRUD + live reads via GetSchedule          │
└──────────┬───────────────────────────────┬─────────────────────────┘
           │                               │
           ▼                               ▼
┌──────────────────────┐    ┌──────────────────────────────────────────┐
│  RDS PostgreSQL       │    │  EventBridge Scheduler Group             │
│  single tenant table  │    │  "pms-schedulers"                        │
│                       │    │                                          │
│  tenant_id  Opera     │    │  ├── tenant-001                          │
│  config: {name:...}   │    │  │   cron(0 7 * * ? *) Asia/Kolkata      │
│                       │    │  │   {"tenantId":"tenant-001"}           │
│  NO schedule fields   │    │  ├── tenant-002                          │
│  Schedule lives only  │    │  │   cron(0 9 * * ? *) America/New_York  │
│  in EventBridge       │    │                                          │
└──────────────────────┘    └──────────────┬───────────────────────────┘
                                            │ Fires at scheduled local time
                                            ▼
                             ┌──────────────────────────────┐
                             │  Trigger Lambda              │
                             │  {"tenantId":"tenant-001"}   │
                             │  1. Load tenant from RDS     │
                             │  2. Push payload to SQS      │
                             └──────────────┬───────────────┘
                                            ▼
                             ┌──────────────────────────────┐
                             │  SQS Queue + DLQ             │
                             └──────────────────────────────┘
```

### Infrastructure components

| Component | Managed by | Purpose |
|-----------|-----------|---------|
| RDS PostgreSQL | Terraform | Tenant identity + non-schedule config JSONB |
| EventBridge Scheduler Group | Terraform | Container for all tenant schedulers |
| Pre-defined tenant schedulers | Terraform (`ignore_changes = all`) | Created once, then owned by the application |
| Dynamic tenant schedulers | API Lambda at runtime | Created by `POST /tenants` |
| API Lambda | Terraform + Go build | CRUD API + EventBridge calls |
| Trigger Lambda | Terraform + Go build | Invoked by EventBridge; pushes to SQS |
| SQS Queue + DLQ | Terraform | Async delivery to downstream consumers |
| API Gateway (HTTP v2) | Terraform | Routes HTTP requests to API Lambda |
| IAM Roles | Terraform | Least-privilege per component |

---

## 4. How Multi-Tenancy Works

### Tenant isolation

One EventBridge Scheduler per tenant. Each fires independently at its own local time.

```
pms-schedulers group
  ├── tenant-001  →  cron(0 7 * * ? *)  Asia/Kolkata
  ├── tenant-002  →  cron(0 9 * * ? *)  America/New_York
  ├── tenant-003  →  cron(0 18 * * ? *) Europe/London
  └── ...
```

The Lambda receives `{"tenantId":"tenant-001"}` — no scanning or polling.

### Database schema

One table — `config` JSONB sits alongside tenant identity. Schedule details are not stored here.

```sql
CREATE TABLE tenant (
    tenant_id    VARCHAR(50)  PRIMARY KEY,
    pms_provider VARCHAR(100) NOT NULL,
    config       JSONB        NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
```

`config` JSONB holds arbitrary non-schedule metadata (hotel name, codes, etc.). It does **not** store schedule fields like cron expression, timezone, or enabled state — those live only in EventBridge.

### Scheduler naming

The EventBridge scheduler name equals the `tenantId` exactly (e.g. `tenant-001`). This is deterministic — the API always knows the scheduler name from the tenant ID alone, with no lookup needed.

### Sources of truth

| Data | Source of truth |
|------|----------------|
| Tenant identity (`pms_provider`) + non-schedule metadata (`config`) | RDS `tenant` table |
| Schedule (`expression`, `timezone`, `state`) | EventBridge Scheduler |

### Tenant lifecycle

```
Create                         Update schedule               Delete
──────                         ───────────────               ──────
POST /tenants                  PUT /tenants/{id}/schedule    DELETE /tenants/{id}
    │                              │                              │
    ├─ EventBridge Upsert          ├─ EventBridge Upsert          ├─ DB delete
    │  (schedule first)            │  (EventBridge ONLY           │
    └─ DB insert                   │   no DB write)               └─ EventBridge delete
```

### Terraform-owned vs API-owned schedulers

Schedulers defined in `terraform/scheduler.tf` use `lifecycle { ignore_changes = all }`:

```hcl
resource "aws_scheduler_schedule" "tenant_001" {
  name       = "tenant-001"
  ...
  lifecycle { ignore_changes = all }
}
```

- **First `terraform apply`**: creates the scheduler
- **Every subsequent `terraform apply`**: skips it entirely — Terraform reads the state but makes no changes
- **API Lambda**: can freely modify or delete it; Terraform will never revert those changes

Schedulers created via `POST /tenants` at runtime are never in Terraform state — they are purely application-managed.

---

## 5. How Scheduling Works

Each tenant's `expression` is passed directly to EventBridge. There is no server-side conversion.

### Cron expression format

EventBridge: `cron(minutes hours day-of-month month day-of-week year)`

| Run time | Expression |
|----------|-----------|
| Every day at 07:00 | `cron(0 7 * * ? *)` |
| Every day at 08:30 | `cron(30 8 * * ? *)` |
| Every day at 18:00 | `cron(0 18 * * ? *)` |
| Every hour | `rate(1 hour)` |
| Every 30 minutes | `rate(30 minutes)` |

`?` in day-of-week means "no specific value" — required when day-of-month is `*`.

### Timezone handling

`ScheduleExpressionTimezone` is set from the `timezone` field. AWS interprets the cron in that timezone and handles DST automatically — no UTC offset math in application code.

### Reading schedule state

`GET /tenants/{id}` calls `scheduler.Get()` which calls `GetSchedule` on EventBridge and returns the live expression, timezone, and state. This is always accurate — there is no cached or stale copy in the DB.

---

## 6. Component Deep-Dives

### API Lambda (`lambdas/api/main.go`)

| Route | DB | EventBridge |
|-------|-----|-------------|
| `GET /tenants` | `ListTenants` | — |
| `POST /tenants` | `CreateTenant` (single INSERT, after) | `Upsert` (first) |
| `GET /tenants/{id}` | `GetTenant` | `Get` (live read) |
| `PUT /tenants/{id}/schedule` | `GetTenant` (existence check only) | `Upsert` |
| `PUT /tenants/{id}/config` | `UpdateConfig` (UPDATE tenant SET config) | — |
| `DELETE /tenants/{id}` | `DeleteTenant` | `Delete` (after) |

Key: `PUT /tenants/{id}/schedule` makes **zero DB writes**. `PUT /tenants/{id}/config` makes **zero EventBridge calls**. These are fully separate operations.

On cold start (`init()`): connects to RDS, runs migration (drops legacy `tenant_config` if present, creates `tenant` if absent).

### Trigger Lambda (`lambdas/trigger/main.go`)

Receives `{"tenantId":"tenant-001"}` from EventBridge.

1. `db.GetTenant(tenantID)` — loads `pmsProvider` from RDS.
2. Builds SQS payload: `{ tenantId, pmsProvider, executedAt }`.
3. `sqsClient.SendMessage()` — pushes to `pms-queue`.

The trigger Lambda never reads schedule data — it only needs to know *who* triggered it.

### Scheduler package (`internal/scheduler/scheduler.go`)

| Function | What it does |
|----------|-------------|
| `Get(tenantID)` | Calls `GetSchedule` → returns live `ScheduleInfo` or `nil` if not found |
| `Upsert(tenantID, expression, timezone, enabled)` | `UpdateSchedule` → fallback to `CreateSchedule` on 404 |
| `Delete(tenantID)` | `DeleteSchedule` — idempotent (ignores 404) |

Scheduler name is always derived as `schedulerName(tenantID) = tenantID`. No name is stored anywhere.

### DB package (`internal/db/db.go`)

Single `tenant` table with a `config` JSONB column. Functions: `Connect`, `Ping`, `Migrate`, `CreateTenant` (single INSERT), `GetTenant`, `ListTenants`, `UpdateConfig` (UPDATE tenant SET config), `DeleteTenant`. No joins, no secondary table.

---

## 7. IAM & Security Model

```
pms-api-lambda-role          (assumed by API Lambda)
  scheduler:CreateSchedule
  scheduler:UpdateSchedule
  scheduler:GetSchedule
  scheduler:DeleteSchedule
  scheduler:ListSchedules
  iam:PassRole → pms-scheduler-execution-role only
  logs:*

pms-scheduler-execution-role  (assumed by EventBridge at fire time)
  lambda:InvokeFunction → pms-trigger ARN only

pms-trigger-lambda-role       (assumed by Trigger Lambda)
  sqs:SendMessage → pms-queue ARN only
  sqs:GetQueueAttributes
  logs:*
```

`iam:PassRole` is required because when the API Lambda calls `CreateSchedule` it hands EventBridge a role ARN to assume at fire time. AWS enforces the caller holds `PassRole` on that role.

---

## 8. Data Flow — Step by Step

### Flow A: Creating a new tenant

```
POST /tenants
{ "tenantId": "tenant-001", "pmsProvider": "Opera",
  "config": { "name": "Grand Hotel", "hotelCode": "GH001" },
  "expression": "cron(0 7 * * ? *)", "timezone": "Asia/Kolkata", "enabled": true }
         │
         ▼
API Lambda
  ├── sched.Upsert("tenant-001", "cron(0 7 * * ? *)", "Asia/Kolkata", true)
  │     → CreateSchedule "tenant-001" in pms-schedulers group
  │
  └── db.CreateTenant("tenant-001", "Opera", config)  [transaction]
        → INSERT INTO tenant VALUES (...)
        → INSERT INTO tenant_config VALUES (...)

Response 201:
{ "tenantId":"tenant-001", "pmsProvider":"Opera",
  "config":{"name":"Grand Hotel","hotelCode":"GH001"},
  "schedule":{"expression":"cron(0 7 * * ? *)","timezone":"Asia/Kolkata","state":"ENABLED"} }
```

### Flow B: Reading a tenant

```
GET /tenants/tenant-001
         │
         ▼
API Lambda
  ├── db.GetTenant("tenant-001")
  │     → SELECT t.*, tc.config FROM tenant t LEFT JOIN tenant_config tc ...
  │
  └── sched.Get("tenant-001")
        → GetSchedule "tenant-001" from EventBridge (live, always current)

Response 200:
{ "tenantId":"tenant-001", "pmsProvider":"Opera",
  "config":{"name":"Grand Hotel","hotelCode":"GH001"},
  "schedule":{"expression":"cron(0 7 * * ? *)","timezone":"Asia/Kolkata","state":"ENABLED"},
  "createdAt":"2026-06-29T10:00:00Z" }
```

### Flow C: Updating a schedule

```
PUT /tenants/tenant-001/schedule
{ "expression": "cron(30 9 * * ? *)", "timezone": "Asia/Kolkata", "enabled": true }
         │
         ▼
API Lambda
  ├── db.GetTenant("tenant-001")  ← existence check only
  │
  └── sched.Upsert("tenant-001", "cron(30 9 * * ? *)", "Asia/Kolkata", true)
        → UpdateSchedule "tenant-001"  (NO DB WRITE)

Response 200: { "status": "updated" }
```

No DB row is written. The new schedule lives only in EventBridge.

### Flow C2: Updating tenant metadata

```
PUT /tenants/tenant-001/config
{ "config": { "name": "Grand Hotel Updated", "hotelCode": "GH001", "region": "APAC" } }
         │
         ▼
API Lambda
  ├── db.GetTenant("tenant-001")  ← existence check only
  │
  └── db.UpdateConfig("tenant-001", config)
        → INSERT INTO tenant_config ... ON CONFLICT DO UPDATE  (NO EVENTBRIDGE CALL)

Response 200: { "status": "updated" }
```

No EventBridge call is made. The schedule is untouched.

### Flow D: Scheduled execution fires

```
Clock reaches 09:30 Asia/Kolkata
         │
         ▼
EventBridge Scheduler "tenant-001" fires
  Assumes pms-scheduler-execution-role
  Invokes pms-trigger Lambda with {"tenantId":"tenant-001"}
         │
         ▼
Trigger Lambda
  ├── db.GetTenant("tenant-001")  → { pmsProvider: "Opera" }
  └── sqsClient.SendMessage → { tenantId, pmsProvider, executedAt }
```

### Flow E: Disabling a tenant

```
PUT /tenants/tenant-001/schedule
{ "expression": "cron(0 7 * * ? *)", "timezone": "Asia/Kolkata", "enabled": false }
         │
         ▼
sched.Upsert(..., enabled=false)
  → UpdateSchedule with State=DISABLED
    Scheduler exists but won't fire.  No DB change.
```

### Flow F: Deleting a tenant

```
DELETE /tenants/tenant-001
         │
         ▼
API Lambda
  ├── db.GetTenant("tenant-001")       ← verify exists
  ├── db.DeleteTenant("tenant-001")    ← remove from RDS; cascades to tenant_config
  └── sched.Delete("tenant-001")       ← remove from EventBridge
```

---

## 9. API Reference

Base URL: `terraform output api_endpoint`

```bash
export BASE_URL=https://abc123.execute-api.us-east-1.amazonaws.com
```

---

### GET /health

```bash
curl $BASE_URL/health
```

**200 OK** `{ "status": "ok" }` | **503** `{ "error": "database unavailable" }`

---

### GET /tenants

Returns tenant identity + config from DB. No schedule details (avoids N calls to EventBridge). Call `GET /tenants/{id}` for schedule info.

```bash
curl $BASE_URL/tenants
```

**200 OK**
```json
[
  { "tenantId": "tenant-001", "pmsProvider": "Opera", "config": { "name": "Grand Hotel" }, "createdAt": "2026-06-29T10:00:00Z" },
  { "tenantId": "tenant-002", "pmsProvider": "Mews",  "config": { "name": "City Inn" },    "createdAt": "2026-06-29T10:01:00Z" }
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
| `tenantId` | string | yes | Primary key. Also used as the EventBridge scheduler name. |
| `pmsProvider` | string | yes | PMS system — e.g. `"Opera"`, `"Mews"`, `"Apaleo"` |
| `config` | object | no | Arbitrary non-schedule metadata stored in tenant_config. |
| `expression` | string | yes | EventBridge cron or rate expression |
| `timezone` | string | yes | IANA timezone — e.g. `"Asia/Kolkata"`, `"America/New_York"` |
| `enabled` | bool | no | `true` = active immediately. Defaults to `false`. |

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

**400** `{ "error": "tenantId is required" }` / `"pmsProvider is required"` / `"expression is required"` / `"timezone is required"`

**500** `{ "error": "scheduler: ..." }`

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

**404** `{ "error": "tenant not found" }`

---

### PUT /tenants/{id}/schedule

Updates the EventBridge Scheduler only. **Zero database writes.** DB is only read to verify the tenant exists.

```bash
curl -X PUT $BASE_URL/tenants/tenant-001/schedule \
  -H "Content-Type: application/json" \
  -d '{
    "expression": "cron(30 8 * * ? *)",
    "timezone":   "Asia/Kolkata",
    "enabled":    true
  }'
```

**Disable without deleting:**
```bash
curl -X PUT $BASE_URL/tenants/tenant-001/schedule \
  -H "Content-Type: application/json" \
  -d '{ "expression": "cron(0 7 * * ? *)", "timezone": "Asia/Kolkata", "enabled": false }'
```

**200 OK** `{ "status": "updated" }`

**400** `{ "error": "expression is required" }` / `"timezone is required"`

**404** `{ "error": "tenant not found" }`

**500** `{ "error": "scheduler: ..." }`

---

### PUT /tenants/{id}/config

Replaces the tenant_config JSONB. **No EventBridge call.** Do not include schedule fields here.

```bash
curl -X PUT $BASE_URL/tenants/tenant-001/config \
  -H "Content-Type: application/json" \
  -d '{
    "config": { "name": "Grand Hotel Updated", "hotelCode": "GH001", "region": "APAC" }
  }'
```

**200 OK** `{ "status": "updated" }`

**400** `{ "error": "config is required" }`

**404** `{ "error": "tenant not found" }`

**500** `{ "error": "..." }`

---

### DELETE /tenants/{id}

Deletes the tenant from DB, then deletes the EventBridge Scheduler.

```bash
curl -X DELETE $BASE_URL/tenants/tenant-001
```

**200 OK** `{ "status": "deleted" }`

**404** `{ "error": "tenant not found" }`

**500** `{ "error": "delete scheduler: ..." }`

---

### Timezone reference

```
Asia/Kolkata          UTC+5:30, no DST
America/New_York      UTC-5 (EST) / UTC-4 (EDT)
Europe/London         UTC+0 (GMT) / UTC+1 (BST)
Asia/Tokyo            UTC+9, no DST
Asia/Dubai            UTC+4, no DST
Australia/Sydney      UTC+10 / UTC+11
Europe/Paris          UTC+1 / UTC+2
Asia/Singapore        UTC+8, no DST
America/Sao_Paulo     UTC-3 / UTC-2
America/Toronto       UTC-5 / UTC-4
```

---

## 10. Deployment Guide

### Prerequisites

- AWS CLI, Go 1.21+, Terraform >= 1.6, `zip` (Linux/macOS) or Git Bash (Windows)

### Step 1 — Configure Terraform

```bash
cd terraform
cp terraform.tfvars.example terraform.tfvars  # set db_password
```

### Step 2 — Build

```bash
make build          # Linux/WSL
.\build.ps1         # Windows PowerShell
```

### Step 3 — Deploy

```bash
make deploy
```

Runs: build → create S3 bucket → upload zips → full `terraform apply`. Takes ~8–10 min (RDS startup).

After apply:
```
api_endpoint  = "https://abc123.execute-api.us-east-1.amazonaws.com"
rds_endpoint  = "pms-postgres.xxxx.us-east-1.rds.amazonaws.com"
```

### Step 4 — Seed

```bash
export API_URL=https://abc123.execute-api.us-east-1.amazonaws.com
make seed
```

### Step 5 — Verify

```bash
# List schedulers
aws scheduler list-schedules --group-name pms-schedulers \
  --query "Schedules[*].{Name:Name,State:State}" --output table

# Get one scheduler's live details
aws scheduler get-schedule --group-name pms-schedulers --name tenant-001 \
  --query "{Expression:ScheduleExpression,Timezone:ScheduleExpressionTimezone,State:State}"
```

### Updating Lambda code

```bash
make build && make upload
cd terraform && terraform apply -auto-approve
```

### Teardown

```bash
cd terraform && terraform destroy
```

---

## 11. Testing & Verification

### Manual trigger

```bash
aws lambda invoke \
  --function-name pms-trigger \
  --payload '{"tenantId":"tenant-001"}' \
  --cli-binary-format raw-in-base64-out \
  response.json && cat response.json
```

### CloudWatch logs

```bash
aws logs tail /aws/lambda/pms-api     --follow
aws logs tail /aws/lambda/pms-trigger --follow | jq .
```

### SQS message

```bash
aws sqs receive-message \
  --queue-url $(cd terraform && terraform output -raw sqs_queue_url) \
  --query "Messages[0].Body" --output text | python3 -m json.tool
```

Expected:
```json
{ "tenantId": "tenant-001", "pmsProvider": "Opera", "executedAt": "2026-06-29T01:30:00Z" }
```

### Verify schedule update

```bash
curl -X PUT $BASE_URL/tenants/tenant-001/schedule \
  -H "Content-Type: application/json" \
  -d '{"expression":"cron(30 9 * * ? *)","timezone":"Asia/Kolkata","enabled":true}'

# Confirm EventBridge was updated
aws scheduler get-schedule --group-name pms-schedulers --name tenant-001 \
  --query "{Expression:ScheduleExpression,Timezone:ScheduleExpressionTimezone}"
# → { "Expression": "cron(30 9 * * ? *)", "Timezone": "Asia/Kolkata" }

# Confirm DB was NOT changed (it has no schedule data to change)
curl $BASE_URL/tenants/tenant-001
# → schedule.expression will show "cron(30 9 * * ? *)" — fetched live from EventBridge
```

---

## 12. Operational Topics

### Scheduler recovery

Since schedule data lives only in EventBridge, recovery means recreating the schedulers. The DB tells you *who* the tenants are; you supply the schedule.

```bash
# Re-PUT schedule for a specific tenant
curl -X PUT $BASE_URL/tenants/tenant-001/schedule \
  -H "Content-Type: application/json" \
  -d '{"expression":"cron(0 7 * * ? *)","timezone":"Asia/Kolkata","enabled":true}'
```

Or run `terraform apply` — pre-defined schedulers in `scheduler.tf` will be recreated if deleted (Terraform detects they're missing in state).

### Disabling a tenant

```bash
# Pause
curl -X PUT $BASE_URL/tenants/tenant-001/schedule \
  -d '{"expression":"cron(0 7 * * ? *)","timezone":"Asia/Kolkata","enabled":false}'

# Resume
curl -X PUT $BASE_URL/tenants/tenant-001/schedule \
  -d '{"expression":"cron(0 7 * * ? *)","timezone":"Asia/Kolkata","enabled":true}'
```

### Dead letter queue

```bash
aws sqs get-queue-attributes \
  --queue-url $(terraform output -raw dlq_url) \
  --attribute-names ApproximateNumberOfMessages
```

Any value > 0 means a tenant's trigger failed all 3 retries.

---

## 13. Limitations & Trade-offs

### 1. One tenant can only have one schedule

`tenant_id` is the primary key AND the EventBridge scheduler name. One tenant maps to exactly one scheduler. A hotel that needs both a 07:00 and an 18:00 job cannot be expressed — it would need two separate tenant records.

### 2. One tenant can only have one PMS provider

`pms_provider` is a single `VARCHAR(100)` column. A hotel group using two systems needs two separate tenant records.

### 3. Schedule data is permanently lost if the EventBridge scheduler is deleted

The schedule (expression, timezone, state) is never stored in the database — EventBridge is the only place it exists. If a scheduler is deleted accidentally (via the AWS Console, CLI, or `terraform destroy`), there is no record anywhere of what the schedule was. Recovery requires someone to know the original expression and timezone and re-enter it manually via `PUT /tenants/{id}/schedule`.

### 4. No audit history for schedule changes

Because schedule data is never written to the database, there is no changelog. If a cron expression is changed via `PUT /tenants/{id}/schedule`, there is no record of what it was before, when it changed, or who changed it. Config changes (`PUT /tenants/{id}/config`) have the same problem — `updated_at` records that a change happened, but not what changed.

### 5. `GET /tenants` cannot show schedule details

Returning schedule info on the list endpoint would require one `GetSchedule` call to EventBridge per tenant — N tenants = N API calls. `ListSchedules` exists but does not return the expression or timezone, only names. So the list endpoint returns only DB data (identity + config) and callers must make a separate `GET /tenants/{id}` per tenant to see the schedule.

### 6. Every `GET /tenants/{id}` makes a live EventBridge API call

Schedule data is never cached in the DB, so reading a single tenant always results in two round trips: one to RDS, one to EventBridge. If EventBridge is unavailable, the endpoint returns a 500 even though the tenant identity and config are fine in the database.

### 7. No distributed transaction between EventBridge and RDS

On `POST /tenants`: the EventBridge scheduler is created first, then the DB transaction runs. If the DB transaction fails and the best-effort rollback (`DeleteSchedule`) also fails, an orphaned scheduler is left running in EventBridge with no matching DB record. The Trigger Lambda will return `tenant not found` on every fire until it is cleaned up manually.

### 8. Best-effort rollback is not guaranteed

The `DeleteSchedule` call on DB failure has no retry. A transient AWS error during rollback leaves an orphaned scheduler silently with no alert.

### 9. Config and schedule are updated on separate paths — no atomic update

`PUT /tenants/{id}/schedule` changes only EventBridge. `PUT /tenants/{id}/config` changes only RDS. There is no single operation that updates both. A failure on the second request leaves the two systems partially updated with no automatic compensation.

### 10. No partial updates to config or schedule

`PUT /tenants/{id}/config` replaces the entire JSONB object — omitting a key drops it silently. `PUT /tenants/{id}/schedule` requires all three fields (`expression`, `timezone`, `enabled`) every time. There is no PATCH for either.

### 11. No cron expression validation

`expression` is passed directly to EventBridge. Invalid expressions (e.g. `"cron(99 25 * * ? *)"`) are only rejected at the EventBridge API call — the error is a raw AWS SDK message, not a user-friendly validation response.

### 12. No optimistic locking

Two concurrent `PUT /tenants/{id}/schedule` requests both succeed — last writer wins with no conflict detection. Same applies to `PUT /tenants/{id}/config`.

### 13. Config JSONB has no schema enforcement

The `config` column accepts any valid JSON. Nothing prevents a caller from accidentally storing schedule fields (`expression`, `timezone`, `enabled`) inside the config object — they will be stored in the DB but have no effect on EventBridge, and will silently diverge from the real schedule.

### 14. RDS connections scale with Lambda concurrency

Each Lambda container holds one PostgreSQL connection. Under high concurrent API traffic, connection count grows until `db.t3.micro` hits its ~85 connection limit. Mitigation: add RDS Proxy.

### 15. No API authentication

All endpoints are publicly accessible to anyone with the API Gateway URL.

---

## 14. Pricing Estimate

**Fixed costs (~$14.72/month):** RDS `db.t3.micro` dominates. All other costs (EventBridge, Lambda, SQS) are negligible at PMS scale due to AWS free tiers.

Adding tenants adds almost nothing to the bill. The cost is flat regardless of whether you have 10 or 500 tenants running daily jobs.

### Production hardening checklist

| Item | Current | Production |
|------|---------|-----------|
| RDS access | Public, 0.0.0.0/0 | Private subnet + RDS Proxy |
| DB credentials | Terraform variable | Secrets Manager + rotation |
| Terraform state | Local | S3 + DynamoDB locking |
| Lambda in VPC | No | Yes — same VPC as RDS |
| API auth | None | JWT / Cognito / IAM authorizer |
| RDS snapshot | Skipped | Enabled |
| RDS deletion protection | Off | On |
