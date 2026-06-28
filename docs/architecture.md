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

- A **PMS provider** (e.g. Opera, Mews, Apaleo)
- A **schedule config** stored as JSON in RDS — containing a name, timezone, cron expression, and enabled flag

At the scheduled time, AWS fires a Lambda function for that specific tenant. The Lambda reads the tenant record from RDS and pushes a JSON payload to an SQS queue for downstream consumers.

The system supports any number of tenants across any timezones, all from a single AWS deployment, with no shared state between tenants at execution time.

---

## 2. The Core Problem It Solves

### The naive approach (what this system avoids)

A common but expensive pattern is a "polling scheduler":

```
Every 1 minute:
  SELECT * FROM tenant WHERE scheduled_time = NOW()
  FOR EACH matching tenant:
    do work
```

Problems with this:
- Lambda runs 1440 times per day just to check if anything needs to run
- Clock drift can cause missed or double-fires
- DST changes require application-level timezone math
- Hard to scale to hundreds of tenants

### This system's approach

One **EventBridge Scheduler per tenant** — a managed AWS resource that fires exactly once per day at the correct local time, with AWS handling all timezone and DST calculations automatically.

```
EventBridge fires at exactly 07:00 Asia/Kolkata
      ↓
Trigger Lambda receives {"tenantId": "tenant-001"}
      ↓
Lambda knows exactly who triggered it — no polling needed
```

---

## 3. System Architecture

### High-level diagram

```
┌─────────────────────────────────────────────────────────────────────┐
│  Client (PMS UI / curl)                                             │
└──────────────────────┬──────────────────────────────────────────────┘
                       │ HTTPS
                       ▼
┌─────────────────────────────────────────────────────────────────────┐
│  API Gateway (HTTP API v2)                                          │
│  POST   /tenants                                                    │
│  PUT    /tenants/{id}/config                                        │
│  GET    /tenants  /tenants/{id}                                     │
│  DELETE /tenants/{id}                                               │
└──────────────────────┬──────────────────────────────────────────────┘
                       │ Lambda Proxy Integration
                       ▼
┌─────────────────────────────────────────────────────────────────────┐
│  API Lambda (Go)                                                    │
│  - Reads / writes RDS tenant + tenant_config tables                 │
│  - Creates / updates / deletes EventBridge Schedulers               │
└──────────┬────────────────────────────┬────────────────────────────┘
           │                            │
           ▼                            ▼
┌─────────────────────┐    ┌────────────────────────────────────────┐
│  RDS PostgreSQL      │    │  EventBridge Scheduler Group           │
│  tenant              │    │  "pms-schedulers"                      │
│  tenant_config       │    │                                        │
│                      │    │  ├── tenant-001-run                    │
│  tenant-001          │    │  │   cron(0 7 * * ? *)                 │
│  tenant-002          │    │  │   timezone: Asia/Kolkata            │
│  tenant-003 ...      │    │  │   payload: {"tenantId":"tenant-001"}│
└─────────────────────┘    └──────────────┬─────────────────────────┘
                                           │ Fires at scheduled local time
   SOURCE OF TRUTH                         ▼
   Schedulers are            ┌──────────────────────────────┐
   derived from              │  Trigger Lambda (Go)         │
   these tables              │  Input: {"tenantId":"tenant-001"} │
                             │                              │
                             │  1. Load tenant from RDS     │
                             │  2. Build JSON payload       │
                             │  3. Push to SQS              │
                             └──────────────┬───────────────┘
                                            │
                                            ▼
                             ┌──────────────────────────────┐
                             │  SQS Queue (pms-queue)       │
                             │  + Dead Letter Queue (DLQ)   │
                             └──────────────────────────────┘
                                            │
                                            ▼
                             ┌──────────────────────────────┐
                             │  Downstream Consumers        │
                             │  (any SQS subscriber)        │
                             └──────────────────────────────┘
```

### Infrastructure components

| Component | Managed by | Purpose |
|-----------|-----------|---------|
| RDS PostgreSQL | Terraform | Source of truth for tenant + config |
| EventBridge Scheduler Group | Terraform | Container for all tenant schedulers |
| Individual tenant schedulers | Application (API Lambda) | One per tenant, fires at their local time |
| API Lambda | Terraform + Go build | CRUD API + scheduler management |
| Trigger Lambda | Terraform + Go build | Invoked by EventBridge; pushes payload to SQS |
| SQS Queue | Terraform | Async delivery to downstream consumers |
| SQS DLQ | Terraform | Catches messages that fail 3 delivery attempts |
| API Gateway (HTTP v2) | Terraform | Routes HTTP requests to API Lambda |
| IAM Roles | Terraform | Least-privilege access for each component |

---

## 4. How Multi-Tenancy Works

### Tenant isolation

Each tenant is completely isolated at the scheduler level. There is **one EventBridge Scheduler per tenant**, not a single shared scheduler that loops through all tenants.

```
AWS Account
└── EventBridge Scheduler Group: pms-schedulers
    ├── tenant-001-run   → fires 07:00 Asia/Kolkata
    ├── tenant-002-run   → fires 09:00 America/New_York
    ├── tenant-003-run   → fires 18:00 Europe/London
    ├── tenant-004-run   → fires 06:00 Asia/Tokyo
    └── ...
```

When `tenant-001`'s scheduler fires, the Lambda receives:

```json
{ "tenantId": "tenant-001" }
```

The Lambda immediately knows which tenant it is working for. No database scan, no polling, no iteration over all tenants.

### Database schema

```sql
-- Parent: one row per tenant
CREATE TABLE tenant (
    tenant_id    VARCHAR(50)  PRIMARY KEY,
    pms_provider VARCHAR(100) NOT NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- Config: JSONB blob keyed by tenant, cascades on delete
CREATE TABLE tenant_config (
    tenant_id  VARCHAR(50) PRIMARY KEY REFERENCES tenant(tenant_id) ON DELETE CASCADE,
    config     JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

`config` JSONB shape:

```json
{
  "name":     "tenant-001-run",
  "timezone": "Asia/Kolkata",
  "cron":     "cron(0 7 * * ? *)",
  "enabled":  true
}
```

`config.name` is used as the EventBridge scheduler name. It must be unique within the scheduler group.

### Why RDS is the source of truth

The EventBridge Scheduler is a **derived resource**. If it gets deleted (accidentally, by Terraform, or during an AWS incident), the data to recreate it is always in RDS:

```sql
SELECT t.tenant_id, tc.config
FROM tenant t
JOIN tenant_config tc ON t.tenant_id = tc.tenant_id
WHERE tc.config->>'enabled' = 'true';
```

A recovery script can loop over these rows and call `PUT /tenants/{id}/config` to recreate all schedulers.

### Tenant lifecycle

```
Create tenant                  Update config                 Delete tenant
─────────────                  ─────────────                 ─────────────
POST /tenants                  PUT /tenants/{id}/config      DELETE /tenants/{id}
    │                              │                              │
    ├─ EventBridge Upsert          ├─ EventBridge Upsert          ├─ DELETE from RDS
    │  (schedule first)            │  (schedule first)            │  (cascades to config)
    │                              │                              │
    └─ INSERT into RDS             └─ UPDATE tenant_config        └─ EventBridge Delete
       tenant + tenant_config
```

**Schedule is always created/updated before the DB write.** On DB failure, the scheduler is deleted as a best-effort rollback.

---

## 5. How Scheduling Works

Each tenant's `config.cron` is passed directly to EventBridge as the schedule expression. There is no server-side conversion.

### Cron expression format

EventBridge uses a 6-field cron: `cron(minutes hours day-of-month month day-of-week year)`.

| Run time | cron expression |
|----------|----------------|
| Every day at 07:00 | `cron(0 7 * * ? *)` |
| Every day at 09:00 | `cron(0 9 * * ? *)` |
| Every day at 07:30 | `cron(30 7 * * ? *)` |
| Every day at 18:00 | `cron(0 18 * * ? *)` |
| Every hour | `rate(1 hour)` |
| Every 30 minutes | `rate(30 minutes)` |

The `?` in `day-of-week` means "no specific value" — required when `day-of-month` is `*`.

### Timezone handling

Every scheduler has a `ScheduleExpressionTimezone` field set from `config.timezone`. The cron expression is interpreted in that timezone — AWS handles DST transitions automatically.

```
Scheduler for tenant-001:
  ScheduleExpression:         cron(0 7 * * ? *)
  ScheduleExpressionTimezone: Asia/Kolkata

AWS fires at 01:30 UTC, which is exactly 07:00 in Kolkata.
```

```
America/New_York — DST example:
  Winter (EST, UTC-5):  09:00 → 14:00 UTC
  Summer (EDT, UTC-4):  09:00 → 13:00 UTC
  Cron stays:           cron(0 9 * * ? *)   — AWS adjusts automatically
```

### The upsert pattern in code

```go
// internal/scheduler/scheduler.go — Upsert()

// Try to update first (fast path — scheduler already exists)
_, err := client.UpdateSchedule(ctx, updateInput)
if err == nil {
    return nil // done
}

// Only create if the scheduler doesn't exist yet
var notFound *types.ResourceNotFoundException
if !errors.As(err, &notFound) {
    return fmt.Errorf("update scheduler: %w", err) // real error
}

// Scheduler didn't exist — create it
_, err = client.CreateSchedule(ctx, createInput)
```

This avoids a `GetSchedule` round-trip. Update is tried first because updating an existing scheduler is more common than creating a new one.

### Schedule name changes

If `config.name` changes on a `PUT /tenants/{id}/config`:
1. The new scheduler is upserted under the new name
2. The old scheduler is deleted by its old name

This keeps EventBridge clean — no orphaned schedulers.

---

## 6. Component Deep-Dives

### API Lambda (`lambdas/api/main.go`)

Handles all HTTP routes. The key responsibility beyond basic CRUD is keeping the EventBridge Scheduler in sync with every DB write, with the **scheduler updated first**:

```
POST   /tenants              → sched.Upsert()  then  db.CreateTenant()
PUT    /tenants/{id}/config  → sched.Upsert()  then  db.UpdateTenantConfig()
DELETE /tenants/{id}         → db.GetTenant()  then  db.DeleteTenant()  then  sched.Delete()
```

On cold start (`init()`), it connects to RDS and runs the migration — idempotent, safe on every cold start.

Routing is done manually by splitting the URL path:

```go
path := strings.TrimRight(req.RequestContext.HTTP.Path, "/")
parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
// parts[0] = "tenants", parts[1] = tenantID, parts[2] = "config"
```

### Trigger Lambda (`lambdas/trigger/main.go`)

The target of every EventBridge Scheduler. Receives a minimal event:

```json
{ "tenantId": "tenant-001" }
```

Execution flow:

```
1. db.GetTenant("tenant-001")
      ↓
   Returns: { pmsProvider: "Opera", config: { name: "...", timezone: "...", ... } }

2. Build SQS payload:
   {
     "tenantId":    "tenant-001",
     "pmsProvider": "Opera",
     "executedAt":  "2026-06-29T01:30:00Z"
   }

3. sqsClient.SendMessage() with MessageAttribute tenantId="tenant-001"
```

In a production PMS system, step 2 would call the PMS provider's API (e.g. OHIP) before pushing to SQS.

### Scheduler package (`internal/scheduler/scheduler.go`)

Two exported functions:

| Function | When called | What it does |
|----------|------------|--------------|
| `Upsert(name, timezone, cronExpr, tenantID, enabled)` | Create or update config | UpdateSchedule → if 404, CreateSchedule. `enabled` sets `State=ENABLED` or `State=DISABLED`. |
| `Delete(name)` | Delete tenant | DeleteSchedule by name (safe if already gone) |

`Upsert` takes the scheduler name directly from `config.name`. The cron expression is passed through as-is — no conversion happens in application code.

### DB package (`internal/db/db.go`)

Uses `pgx/v5/stdlib` — the modern PostgreSQL driver for Go. The `database/sql` standard interface is used so the rest of the code has no pgx dependency.

`CreateTenant` wraps both inserts (`tenant` + `tenant_config`) in a transaction so either both succeed or neither does.

Connection is established once per Lambda container (in `init()`). Lambda reuses the same container across warm invocations, so the connection is pooled effectively without a connection pooler.

---

## 7. IAM & Security Model

Three IAM roles are created by Terraform:

```
┌─────────────────────────────────────────────────────────────────┐
│ pms-api-lambda-role                                             │
│   Assumed by: lambda.amazonaws.com                              │
│   Permissions:                                                  │
│     - logs:* (CloudWatch)                                       │
│     - scheduler:CreateSchedule                                  │
│     - scheduler:UpdateSchedule                                  │
│     - scheduler:DeleteSchedule                                  │
│     - scheduler:GetSchedule                                     │
│     - scheduler:ListSchedules                                   │
│     - iam:PassRole → pms-scheduler-execution-role only          │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│ pms-trigger-lambda-role                                         │
│   Assumed by: lambda.amazonaws.com                              │
│   Permissions:                                                  │
│     - logs:* (CloudWatch)                                       │
│     - sqs:SendMessage → pms-queue only                          │
│     - sqs:GetQueueAttributes → pms-queue only                   │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│ pms-scheduler-execution-role                                    │
│   Assumed by: scheduler.amazonaws.com                           │
│   Permissions:                                                  │
│     - lambda:InvokeFunction → pms-trigger Lambda only           │
└─────────────────────────────────────────────────────────────────┘
```

### Why `iam:PassRole` is required

When the API Lambda calls `CreateSchedule`, it must specify a `RoleArn` that EventBridge will assume when the schedule fires. AWS enforces that the caller has `iam:PassRole` on that specific role. Without this, `CreateSchedule` returns `AccessDenied`.

---

## 8. Data Flow — Step by Step

### Flow A: Creating a new tenant

```
POST /tenants
{
  "tenantId":    "tenant-001",
  "pmsProvider": "Opera",
  "config": {
    "name":     "tenant-001-run",
    "timezone": "Asia/Kolkata",
    "cron":     "cron(0 7 * * ? *)",
    "enabled":  true
  }
}
         │
         ▼
API Lambda
  ├── sched.Upsert("tenant-001-run", "Asia/Kolkata", "cron(0 7 * * ? *)", "tenant-001", true)
  │     ├── Try UpdateSchedule → 404 ResourceNotFoundException
  │     └── CreateSchedule
  │           Name:       "tenant-001-run"
  │           GroupName:  "pms-schedulers"
  │           Expression: "cron(0 7 * * ? *)"
  │           Timezone:   "Asia/Kolkata"
  │           Target:     pms-trigger Lambda
  │           Input:      {"tenantId":"tenant-001"}
  │
  └── db.CreateTenant("tenant-001", "Opera", config)  [transaction]
        INSERT INTO tenant (tenant_id, pms_provider) VALUES (...)
        INSERT INTO tenant_config (tenant_id, config) VALUES (...)
         │
         ▼
Response: HTTP 201
{
  "tenantId":    "tenant-001",
  "pmsProvider": "Opera",
  "config": { "name": "tenant-001-run", "timezone": "Asia/Kolkata", ... }
}
```

### Flow B: Tenant changes their schedule

```
PUT /tenants/tenant-001/config
{
  "config": {
    "name":     "tenant-001-run",
    "timezone": "Asia/Kolkata",
    "cron":     "cron(30 9 * * ? *)",
    "enabled":  true
  }
}
         │
         ▼
API Lambda
  ├── db.GetTenant("tenant-001")  ← fetch old name to detect renames
  │     oldName = "tenant-001-run"
  │
  ├── sched.Upsert("tenant-001-run", "Asia/Kolkata", "cron(30 9 * * ? *)", "tenant-001", true)
  │     └── UpdateSchedule (name unchanged, expression changed)
  │
  └── db.UpdateTenantConfig("tenant-001", newConfig)
         │
         ▼
Response: HTTP 200 { "status": "updated" }

No Terraform changes. No deployment. Effective immediately.
```

### Flow C: Scheduled execution fires

```
Clock reaches 09:30 Asia/Kolkata (= 04:00 UTC)
         │
         ▼
EventBridge Scheduler "tenant-001-run" fires
         │  Assumes pms-scheduler-execution-role
         │  Invokes pms-trigger Lambda
         ▼
Trigger Lambda receives:
{ "tenantId": "tenant-001" }
         │
         ▼
db.GetTenant("tenant-001")
→ { pmsProvider: "Opera", config: { name: "tenant-001-run", ... } }
         │
         ▼
Build payload:
{
  "tenantId":    "tenant-001",
  "pmsProvider": "Opera",
  "executedAt":  "2026-06-29T04:00:00Z"
}
         │
         ▼
SQS SendMessage → pms-queue
  MessageAttribute: tenantId = "tenant-001"
         │
         ▼
Downstream consumer receives and processes the message
```

### Flow D: Disabling a tenant

```
PUT /tenants/tenant-001/config
{
  "config": {
    "name":     "tenant-001-run",
    "timezone": "Asia/Kolkata",
    "cron":     "cron(0 7 * * ? *)",
    "enabled":  false
  }
}
         │
         ▼
API Lambda
  └── sched.Upsert(..., enabled=false)
        └── UpdateSchedule with State=DISABLED
              scheduler still exists, just paused
```

### Flow E: Deleting a tenant

```
DELETE /tenants/tenant-001
         │
         ▼
API Lambda
  ├── db.GetTenant("tenant-001")   ← fetch config.name for scheduler name
  ├── db.DeleteTenant("tenant-001") ← cascades to tenant_config
  └── sched.Delete("tenant-001-run") ← removes EventBridge scheduler
```

---

## 9. API Reference

Base URL: `https://{api-id}.execute-api.{region}.amazonaws.com`

Get it after deployment: `terraform output api_endpoint`

Set it as a variable for the curl examples below:

```bash
export BASE_URL=https://abc123.execute-api.us-east-1.amazonaws.com
```

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
    "tenantId":    "tenant-001",
    "pmsProvider": "Opera",
    "config": {
      "name":     "tenant-001-run",
      "timezone": "Asia/Kolkata",
      "cron":     "cron(0 7 * * ? *)",
      "enabled":  true
    },
    "createdAt": "2026-06-29T10:00:00Z",
    "updatedAt": "2026-06-29T10:00:00Z"
  }
]
```

Returns `[]` when no tenants exist.

---

### POST /tenants

Creates the tenant, its config, and an EventBridge Scheduler.
**Scheduler is created first.** If the DB write fails, the scheduler is deleted as a rollback.

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
| `config.name` | string | yes | EventBridge scheduler name — must be unique within the group |
| `config.timezone` | string | yes | IANA timezone — e.g. `"Asia/Kolkata"`, `"America/New_York"` |
| `config.cron` | string | yes | Full EventBridge expression — e.g. `"cron(0 7 * * ? *)"` or `"rate(1 hour)"` |
| `config.enabled` | bool | no | `true` to activate, `false` to create disabled. Defaults to `false` if omitted |

> **Fields that no longer exist:** `name`, `scheduleType`, `runTime`, `rateMinutes`, `latitude`, `longitude`, `hotelCode`.
> All schedule details are now inside the `config` object.

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

**400 Bad Request**
```json
{ "error": "tenantId is required" }
{ "error": "pmsProvider is required" }
{ "error": "config.name is required" }
{ "error": "config.timezone is required" }
{ "error": "config.cron is required" }
```

**500 Internal Server Error**
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

Replaces the config JSON and updates the EventBridge Scheduler.
**Scheduler is updated first.** If `config.name` changes, the old scheduler is deleted after the new one is created.

```bash
curl -X PUT $BASE_URL/tenants/tenant-001/config \
  -H "Content-Type: application/json" \
  -d '{
    "config": {
      "name":     "tenant-001-run",
      "timezone": "Asia/Kolkata",
      "cron":     "cron(30 8 * * ? *)",
      "enabled":  true
    }
  }'
```

**Disable the scheduler (pause without deleting):**

```bash
curl -X PUT $BASE_URL/tenants/tenant-001/config \
  -H "Content-Type: application/json" \
  -d '{
    "config": {
      "name":     "tenant-001-run",
      "timezone": "Asia/Kolkata",
      "cron":     "cron(0 7 * * ? *)",
      "enabled":  false
    }
  }'
```

**200 OK**
```json
{ "status": "updated" }
```

**400 Bad Request** — same field errors as POST.

**404 Not Found**
```json
{ "error": "tenant not found" }
```

---

### DELETE /tenants/{id}

Deletes the tenant row (cascades to `tenant_config`), then deletes the EventBridge Scheduler.

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

---

### Timezone names

Use IANA timezone names. Common examples:

```
Asia/Kolkata          UTC+5:30, no DST
America/New_York      UTC-5 (EST) / UTC-4 (EDT)
Europe/London         UTC+0 (GMT) / UTC+1 (BST)
Asia/Tokyo            UTC+9, no DST
Asia/Dubai            UTC+4, no DST
Australia/Sydney      UTC+10 (AEST) / UTC+11 (AEDT)
Europe/Paris          UTC+1 (CET) / UTC+2 (CEST)
Asia/Singapore        UTC+8, no DST
America/Sao_Paulo     UTC-3 (BRT) / UTC-2 (BRST)
America/Toronto       UTC-5 (EST) / UTC-4 (EDT)
UTC                   Always UTC+0
```

Full list: https://en.wikipedia.org/wiki/List_of_tz_database_time_zones

---

## 10. Deployment Guide

### Prerequisites

- AWS CLI configured with an IAM user/role that can create Lambda, RDS, EventBridge, SQS, IAM, API Gateway resources
- Go 1.21+
- Terraform >= 1.6
- `zip` command (Linux/macOS) or Git Bash on Windows

---

### Step 1 — Configure Terraform variables

```bash
cd terraform
cp terraform.tfvars.example terraform.tfvars
```

Edit `terraform.tfvars`:

```hcl
aws_region  = "us-east-1"
project     = "pms"
db_username = "pmsadmin"
db_password = "YourStrongPassword123!"
db_name     = "pmsdb"
```

---

### Step 2 — Build Lambda binaries

**Linux / WSL (Makefile):**
```bash
make build
```
Outputs to `/tmp/pms-build/` (WSL-native filesystem — avoids NTFS permission errors).

**Windows PowerShell (native):**
```powershell
.\build.ps1
```
Outputs to `bin\` in the project directory.

> **Note:** `terraform plan` reads the zip via `filebase64sha256` at plan time, so the zips must exist before running `terraform plan`.

---

### Step 3 — Deploy infrastructure

**Recommended — use `make deploy` (handles all phases automatically):**
```bash
make deploy
```

`make deploy` runs in order:
1. `make build` — compiles and zips both Lambdas
2. `terraform apply -target=aws_s3_bucket.lambda_artifacts` — creates the S3 bucket first
3. `make upload` — uploads both zips to S3
4. `terraform apply` — creates all remaining resources

Total deploy time is ~8–10 minutes, mostly waiting for RDS.

After `apply`:

```
api_endpoint           = "https://abc123.execute-api.us-east-1.amazonaws.com"
rds_endpoint           = "pms-postgres.xxxx.us-east-1.rds.amazonaws.com"
sqs_queue_url          = "https://sqs.us-east-1.amazonaws.com/123456/pms-queue"
scheduler_group_name   = "pms-schedulers"
trigger_lambda_arn     = "arn:aws:lambda:us-east-1:123456:function:pms-trigger"
lambda_bucket          = "pms-lambda-<account-id>"
```

---

### Step 4 — Seed 10 tenants

```bash
export API_URL=https://abc123.execute-api.us-east-1.amazonaws.com
make seed
```

**Windows PowerShell:**
```powershell
$env:API_URL = "https://abc123.execute-api.us-east-1.amazonaws.com"
go run ./scripts/seed
```

Expected output:
```
✓ tenant-001 | Opera        | cron(0 7  * * ? *) | Asia/Kolkata
✓ tenant-002 | Mews         | cron(0 9  * * ? *) | America/New_York
✓ tenant-003 | Apaleo       | cron(0 18 * * ? *) | Europe/London
...
Done. created=10 failed=0
```

---

### Step 5 — Verify schedulers were created

```bash
aws scheduler list-schedules \
  --group-name pms-schedulers \
  --query "Schedules[*].{Name:Name,State:State}" \
  --output table
```

Get the expression and timezone for a specific scheduler:

```bash
aws scheduler get-schedule \
  --group-name pms-schedulers \
  --name tenant-001-run \
  --query "{Expression:ScheduleExpression,Timezone:ScheduleExpressionTimezone,State:State}"
```

Expected:
```json
{
    "Expression": "cron(0 7 * * ? *)",
    "Timezone": "Asia/Kolkata",
    "State": "ENABLED"
}
```

---

### Updating Lambda code

```bash
make build && make upload
cd terraform && terraform apply -auto-approve
```

Or:
```bash
make deploy
```

---

### Teardown

```bash
cd terraform && terraform destroy
```

RDS data is lost (`skip_final_snapshot = true`).

---

## 11. Testing & Verification

### Manual trigger — fire a Lambda immediately

```bash
aws lambda invoke \
  --function-name pms-trigger \
  --payload '{"tenantId":"tenant-001"}' \
  --cli-binary-format raw-in-base64-out \
  response.json

cat response.json
# null means success
```

### Check CloudWatch logs

```bash
aws logs tail /aws/lambda/pms-api     --follow
aws logs tail /aws/lambda/pms-trigger --follow
```

After a trigger invocation:

```json
{"level":"INFO","msg":"trigger started","tenantId":"tenant-001"}
{"level":"INFO","msg":"db: tenant loaded","tenantId":"tenant-001","pmsProvider":"Opera","scheduleName":"tenant-001-run"}
{"level":"INFO","msg":"trigger complete","tenantId":"tenant-001","sqsMessageId":"abc-12345","durationMs":120}
```

### Read messages from SQS

```bash
aws sqs receive-message \
  --queue-url $(cd terraform && terraform output -raw sqs_queue_url) \
  --message-attribute-names tenantId \
  --query "Messages[0].Body" \
  --output text | python3 -m json.tool
```

Expected output:

```json
{
  "tenantId":    "tenant-001",
  "pmsProvider": "Opera",
  "executedAt":  "2026-06-29T01:30:00Z"
}
```

### Verify a schedule update takes effect immediately

```bash
curl -X PUT $BASE_URL/tenants/tenant-001/config \
  -H "Content-Type: application/json" \
  -d '{"config":{"name":"tenant-001-run","timezone":"Asia/Kolkata","cron":"cron(30 9 * * ? *)","enabled":true}}'

aws scheduler get-schedule \
  --group-name pms-schedulers \
  --name tenant-001-run \
  --query "{Expression:ScheduleExpression,Timezone:ScheduleExpressionTimezone}"
```

Expected: `{ "Expression": "cron(30 9 * * ? *)", "Timezone": "Asia/Kolkata" }`

---

## 12. Operational Topics

### Scheduler recovery

If schedulers are accidentally deleted:

```bash
# 1. List all enabled tenants
psql "postgresql://pmsadmin:<pw>@<rds-endpoint>:5432/pmsdb" \
  -c "SELECT t.tenant_id, tc.config FROM tenant t JOIN tenant_config tc ON t.tenant_id = tc.tenant_id WHERE tc.config->>'enabled' = 'true';"

# 2. Re-PUT each tenant's config to recreate their scheduler
curl -X PUT $BASE_URL/tenants/tenant-001/config \
  -H "Content-Type: application/json" \
  -d '{"config":{"name":"tenant-001-run","timezone":"Asia/Kolkata","cron":"cron(0 7 * * ? *)","enabled":true}}'
```

### Dead letter queue monitoring

```bash
aws sqs get-queue-attributes \
  --queue-url $(terraform output -raw dlq_url) \
  --attribute-names ApproximateNumberOfMessages
```

Any value above 0 means a tenant's job failed all 3 retries.

### Disabling a tenant temporarily

```bash
# Pause
curl -X PUT $BASE_URL/tenants/tenant-003/config \
  -H "Content-Type: application/json" \
  -d '{"config":{"name":"tenant-003-run","timezone":"Europe/London","cron":"cron(0 18 * * ? *)","enabled":false}}'

# Resume
curl -X PUT $BASE_URL/tenants/tenant-003/config \
  -H "Content-Type: application/json" \
  -d '{"config":{"name":"tenant-003-run","timezone":"Europe/London","cron":"cron(0 18 * * ? *)","enabled":true}}'
```

### Scaling to hundreds of tenants

Each tenant is one EventBridge Scheduler. AWS supports up to 1,000,000 schedulers per account. The system scales linearly — no code changes required.

---

## 13. Limitations & Trade-offs

These are known weaknesses in the current design. They are acceptable for a simple dev/internal system but should be addressed before production use at scale.

---

### 1. One tenant can only have one schedule

`tenant_config` uses `tenant_id` as its PRIMARY KEY, enforcing a strict one-to-one relationship between a tenant and its config. A tenant cannot have multiple schedules — for example, a hotel that wants to run a job at 07:00 **and** at 18:00 every day cannot be represented. The entire config blob is replaced on every `PUT /tenants/{id}/config`, so there is no way to add a second schedule without redesigning the schema.

**What would be needed to fix this:** Change `tenant_config` to a one-to-many table (drop the PK constraint, add a surrogate key, allow multiple rows per `tenant_id`), and update the API and EventBridge scheduler naming accordingly.

---

### 2. One tenant can only have one PMS provider

`pms_provider` is a single `VARCHAR(100)` column on the `tenant` row. A tenant is locked to one provider. If a hotel group uses both Opera for front-desk and Mews for F&B, they cannot be represented as one tenant — they would need two separate tenant records with duplicate scheduling config.

**What would be needed to fix this:** Move `pms_provider` out of the `tenant` table into a separate `tenant_provider` join table (many-to-many), and decide how the Trigger Lambda should handle multiple providers per invocation.

---

### 3. No distributed transaction between EventBridge and RDS (no atomicity)

EventBridge and RDS are two separate systems. There is no way to make a write to both atomic. The current approach — schedule first, then DB — means three failure modes exist:

| Scenario | Result |
|----------|--------|
| EventBridge succeeds, DB succeeds | ✓ Consistent |
| EventBridge fails | DB write is skipped. Consistent — no orphan. |
| EventBridge succeeds, DB fails, rollback (Delete) succeeds | Consistent — scheduler cleaned up. |
| EventBridge succeeds, DB fails, rollback (Delete) also fails | **Orphaned EventBridge scheduler** with no DB record. It will fire indefinitely and the Trigger Lambda will return `tenant not found` every time. |

There is no automatic detection or cleanup of orphaned schedulers. Manual intervention (listing EventBridge schedulers and comparing against the DB) is required.

---

### 4. Best-effort rollback is not guaranteed

The scheduler delete on DB failure is a best-effort call with no retry. If the EventBridge `DeleteSchedule` call fails (network issue, throttle, IAM transient error), the orphaned scheduler is silently left behind. There is no dead-letter mechanism for failed rollbacks.

---

### 5. `config.name` must be globally unique within the scheduler group

EventBridge Scheduler names must be unique within a group. If two tenants are created with the same `config.name`, the second `CreateSchedule` will succeed (it will call `UpdateSchedule` and overwrite the first tenant's scheduler). Both tenants will share one scheduler — only one will fire correctly.

There is no uniqueness constraint on `config->>'name'` in the database, so this error is only caught at runtime by observing wrong behaviour.

---

### 6. No partial config updates

`PUT /tenants/{id}/config` replaces the entire config JSON blob. If you only want to change the `cron` field, you must still resend `name`, `timezone`, and `enabled`. There is no PATCH endpoint. Forgetting a field will silently drop it from the stored config.

---

### 7. No cron expression validation

`config.cron` is passed directly to EventBridge without any format checking in the API. An invalid expression (e.g. `"cron(99 25 * * ? *)"`) is only caught when EventBridge rejects it, and the error message returned is an AWS SDK error, not a user-friendly validation message.

---

### 8. Brief gap during scheduler rename

When `config.name` changes on a `PUT /tenants/{id}/config`, the sequence is:
1. Create new scheduler (new name)
2. Delete old scheduler (old name)

Between steps 1 and 2 both schedulers exist simultaneously. If step 2 fails, the old scheduler is orphaned. More critically, if the scheduled time falls exactly in this window, the job may fire twice — once from each scheduler.

---

### 9. No optimistic locking — concurrent updates silently overwrite each other

Two simultaneous `PUT /tenants/{id}/config` requests will both succeed. The last writer wins with no conflict detection. There is no ETag, version field, or row locking. In a multi-user system this can silently lose config changes.

---

### 10. JSONB config is harder to query and constrain

Storing schedule details as JSONB means:

- **No column-level constraints** — `NOT NULL`, `CHECK`, and foreign keys cannot be applied to individual config fields. Validation lives only in application code and can drift.
- **Harder to filter in SQL** — queries like "find all tenants in Asia/Kolkata" require JSONB operators (`tc.config->>'timezone' = 'Asia/Kolkata'`) rather than a simple `WHERE timezone = 'Asia/Kolkata'`.
- **No index by default** — JSONB fields are not indexed unless a GIN index is explicitly created.

---

### 11. No audit history

`updated_at` records when the config was last changed, but not what it was before or who changed it. There is no changelog table, no versioning, and no way to roll back a config to a previous value without restoring a DB snapshot.

---

### 12. Trigger Lambda execution window race on create

On `POST /tenants`, the sequence is:
1. EventBridge scheduler created
2. DB transaction committed

If EventBridge fires (extremely unlikely but theoretically possible if the cron fires at the exact moment of creation) before step 2 commits, the Trigger Lambda will call `db.GetTenant()` and get `tenant not found`, causing the invocation to fail. This is an inherent consequence of the "schedule first" ordering.

---

### 13. RDS connection count scales with concurrent Lambda instances

Each Lambda container holds one open PostgreSQL connection. Under a burst of concurrent API calls (many tenants being created simultaneously), many Lambda containers spin up in parallel, each opening a new connection. RDS `db.t3.micro` supports ~85 connections maximum. At scale, this will cause `too many connections` errors.

**Mitigation:** Add RDS Proxy in front of the database to pool connections at the infrastructure level.

---

### 14. No API authentication

All endpoints are publicly accessible. Anyone with the API Gateway URL can create, update, or delete tenants. There is no API key, JWT, or IAM authorizer.

---

## 14. Pricing Estimate

All prices are **us-east-1, on-demand**. Assumes 30-day month.

| Component | Monthly (fixed) |
|---|---|
| RDS db.t3.micro (PostgreSQL 15, 20 GB gp2) | ~$14.71 |
| S3, API Gateway, CloudWatch Logs | < $0.10 |

Variable costs per tenant invocation are negligible at typical PMS scale — Lambda, SQS, and EventBridge Scheduler all have free tiers that cover thousands of daily invocations.

**RDS is the dominant cost** regardless of tenant count. Going from 10 to 1,000 tenants adds less than $1/month in variable costs.

### Production hardening checklist

| Item | Current (dev) | Production recommendation |
|------|--------------|--------------------------|
| RDS access | Publicly accessible | Private subnet + RDS Proxy |
| RDS security group | Open to 0.0.0.0/0 | Restrict to Lambda security group only |
| DB credentials | Terraform variable | AWS Secrets Manager + rotation |
| Terraform state | Local | S3 backend + DynamoDB locking |
| Lambda in VPC | No | Yes — same VPC as RDS |
| API authentication | None | API Gateway authorizer (JWT/Cognito/IAM) |
| RDS snapshot | `skip_final_snapshot = true` | `skip_final_snapshot = false` |
| RDS deletion protection | `false` | `true` |
