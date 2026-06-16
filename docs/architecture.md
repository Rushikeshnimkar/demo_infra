# Multi-Tenant EventBridge Scheduler — Architecture & Usage Guide

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
13. [Pricing Estimate](#13-pricing-estimate)

---

## 1. What This System Does

This system schedules a daily job for each hotel tenant in a Property Management System (PMS). Each hotel has its own:

- **Preferred run time** (e.g., 07:00, 18:00)
- **Timezone** (e.g., `Asia/Kolkata`, `America/New_York`)

At the scheduled time, AWS fires a Lambda function for that specific tenant. The Lambda fetches data from an external API and forwards a JSON payload to an SQS queue for downstream consumers.

The system currently runs **10 hotel tenants** across 10 different timezones, all from a single AWS deployment, with no shared state between tenants at execution time.

---

## 2. The Core Problem It Solves

### The naive approach (what this system avoids)

A common but expensive pattern is a "polling scheduler":

```
Every 1 minute:
  SELECT * FROM tenant_config WHERE run_time = NOW()
  FOR EACH matching tenant:
    invoke OHIP API
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
│  POST /tenants                                                      │
│  PUT  /tenants/{id}/schedule                                        │
│  GET  /tenants / /tenants/{id}                                      │
│  DELETE /tenants/{id}                                               │
└──────────────────────┬──────────────────────────────────────────────┘
                       │ Lambda Proxy Integration
                       ▼
┌─────────────────────────────────────────────────────────────────────┐
│  API Lambda (Go)                                                    │
│  - Reads / writes RDS tenant_config table                           │
│  - Creates / updates / deletes EventBridge Schedulers               │
└──────────┬────────────────────────────┬────────────────────────────┘
           │                            │
           ▼                            ▼
┌─────────────────────┐    ┌────────────────────────────────────────┐
│  RDS PostgreSQL      │    │  EventBridge Scheduler Group           │
│  tenant_config table │    │  "pms-schedulers"                      │
│                      │    │                                        │
│  tenant-001          │    │  ├── tenant-tenant-001                 │
│  tenant-002          │    │  │   cron(0 7 * * ? *)                 │
│  tenant-003 ...      │    │  │   timezone: Asia/Kolkata            │
│                      │    │  │   payload: {"tenantId":"tenant-001"}│
└─────────────────────┘    │  │                                     │
                            │  ├── tenant-tenant-002                 │
   SOURCE OF TRUTH          │  │   cron(0 9 * * ? *)                 │
   Schedulers are           │  │   timezone: America/New_York        │
   derived from             │  │                                     │
   this table               │  └── tenant-tenant-010 ...             │
                            └──────────────┬─────────────────────────┘
                                           │ Fires at scheduled local time
                                           ▼
                            ┌──────────────────────────────┐
                            │  Trigger Lambda (Go)         │
                            │  Input: {"tenantId":"tenant-001"} │
                            │                              │
                            │  1. Load tenant from RDS     │
                            │  2. Call Open-Meteo API      │
                            │  3. Build JSON payload       │
                            │  4. Push to SQS              │
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
| RDS PostgreSQL | Terraform | Source of truth for tenant config |
| EventBridge Scheduler Group | Terraform | Container for all tenant schedulers |
| Individual tenant schedulers | Application (API Lambda) | One per tenant, fires at their local time |
| API Lambda | Terraform + Go build | CRUD API + scheduler management |
| Trigger Lambda | Terraform + Go build | Invoked by EventBridge; calls external API + SQS |
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
    ├── tenant-tenant-001   → fires 07:00 Asia/Kolkata
    ├── tenant-tenant-002   → fires 09:00 America/New_York
    ├── tenant-tenant-003   → fires 18:00 Europe/London
    ├── tenant-tenant-004   → fires 06:00 Asia/Tokyo
    ├── tenant-tenant-005   → fires 08:00 Asia/Dubai
    ├── tenant-tenant-006   → fires 10:00 Australia/Sydney
    ├── tenant-tenant-007   → fires 20:00 Europe/Paris
    ├── tenant-tenant-008   → fires 11:00 Asia/Singapore
    ├── tenant-tenant-009   → fires 14:00 America/Sao_Paulo
    └── tenant-tenant-010   → fires 07:30 America/Toronto
```

When `tenant-001`'s scheduler fires, the Lambda receives:

```json
{ "tenantId": "tenant-001" }
```

The Lambda immediately knows which tenant it is working for. No database scan, no polling, no iteration over all tenants.

### Database schema

```sql
CREATE TABLE tenant_config (
    tenant_id     VARCHAR(50)   PRIMARY KEY,    -- "tenant-001"
    name          VARCHAR(255)  NOT NULL,        -- "Grand Hotel Mumbai"
    schedule_type VARCHAR(10)   NOT NULL DEFAULT 'daily',  -- "daily" or "rate"
    timezone      VARCHAR(100)  NOT NULL DEFAULT 'UTC',    -- "Asia/Kolkata" (daily only)
    run_time      VARCHAR(5)    NOT NULL DEFAULT '',       -- "07:00" (daily only)
    rate_minutes  INT           NOT NULL DEFAULT 0,        -- 60 = every 1 hour (rate only)
    enabled       BOOLEAN       NOT NULL,                  -- true / false
    latitude      DECIMAL(9,6)  NOT NULL,                  -- 19.076000
    longitude     DECIMAL(9,6)  NOT NULL,                  -- 72.877700
    hotel_code    VARCHAR(50),                             -- "H001"
    created_at    TIMESTAMPTZ   NOT NULL,
    updated_at    TIMESTAMPTZ   NOT NULL
);
```

### Why RDS is the source of truth

The EventBridge Scheduler is considered a **derived resource**. If it gets deleted (accidentally, by Terraform destroy, or during an AWS incident), the data to recreate it is always in RDS:

```sql
SELECT tenant_id, schedule_type, timezone, run_time, rate_minutes
FROM tenant_config
WHERE enabled = true;
```

A recovery script can loop over these rows and call `scheduler.Upsert()` to bring all schedulers back. The schedulers being gone does not cause data loss — it only causes missed executions until they are restored.

### Tenant lifecycle

```
Create tenant                 Update schedule               Delete tenant
─────────────                ───────────────               ─────────────
POST /tenants                PUT /tenants/{id}/schedule    DELETE /tenants/{id}
    │                            │                              │
    ├─ INSERT into RDS           ├─ UPDATE RDS row              ├─ DELETE from RDS
    │                            │                              │
    └─ CreateSchedule            └─ UpdateSchedule              └─ DeleteSchedule
       (EventBridge)                (EventBridge)                  (EventBridge)
```

Both DB and scheduler are always updated atomically in the same API call — they stay in sync.

---

## 5. How Scheduling Works

Each tenant has a `schedule_type` of either `"daily"` or `"rate"`, stored in RDS and translated into an EventBridge Scheduler expression.

### schedule_type: "daily" — fixed time each day

The `run_time` field (e.g., `"07:30"`) is converted into an EventBridge cron expression at write time:

```
run_time "HH:MM"
           │
           ▼
    cron(MM HH * * ? *)
```

Examples:

| run_time | cron expression | Meaning |
|----------|----------------|---------|
| `07:00` | `cron(0 7 * * ? *)` | Every day at 07:00 |
| `09:00` | `cron(0 9 * * ? *)` | Every day at 09:00 |
| `07:30` | `cron(30 7 * * ? *)` | Every day at 07:30 |
| `20:00` | `cron(0 20 * * ? *)` | Every day at 20:00 |

Cron field order: `cron(Minutes  Hours  Day-of-month  Month  Day-of-week  Year)`

The `?` in `Day-of-week` means "no specific value" — required when `Day-of-month` is `*`.

**Timezone handling:** Every daily scheduler has a `ScheduleExpressionTimezone` field. The cron expression is interpreted in that timezone, not UTC. AWS handles DST transitions automatically — the application never computes UTC offsets.

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

### schedule_type: "rate" — recurring interval

The `rate_minutes` field is converted to a rate expression:

| rate_minutes | rate expression | Meaning |
|---|---|---|
| `1` | `rate(1 minute)` | Every minute |
| `30` | `rate(30 minutes)` | Every 30 minutes |
| `60` | `rate(1 hour)` | Every hour |
| `120` | `rate(2 hours)` | Every 2 hours |

Rate expressions fire on a fixed interval from when the scheduler was created or last updated. They are timezone-agnostic — `ScheduleExpressionTimezone` is set to `UTC` and has no effect.

```
Scheduler for a rate tenant:
  ScheduleExpression:         rate(1 hour)
  ScheduleExpressionTimezone: UTC   ← ignored for rate expressions
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

This avoids a redundant `GetSchedule` round-trip. Update is tried first because the hot path for an existing tenant changing their schedule is more common than a brand-new tenant being created.

---

## 6. Component Deep-Dives

### API Lambda (`lambdas/api/main.go`)

Handles all five HTTP routes. The key responsibility beyond basic CRUD is keeping the EventBridge Scheduler in sync with every DB write:

```
POST   /tenants           → db.CreateTenant()  + sched.Upsert()
PUT    /tenants/{id}/schedule → db.UpdateTenantSchedule() + sched.Upsert() or sched.Disable()
DELETE /tenants/{id}      → db.DeleteTenant()  + sched.Delete()
```

On cold start (`init()`), it connects to RDS and runs `CREATE TABLE IF NOT EXISTS` — idempotent, so safe to run on every cold start.

Routing is done manually (no external router library) by splitting the URL path:

```go
path := strings.TrimRight(req.RequestContext.HTTP.Path, "/")
parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
// parts[0] = "tenants", parts[1] = tenantID, parts[2] = "schedule"
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
   Returns: { timezone: "Asia/Kolkata", latitude: 19.076, longitude: 72.877, hotelCode: "H001", ... }

2. GET https://api.open-meteo.com/v1/forecast?latitude=19.076&longitude=72.877
       &current=temperature_2m,wind_speed_10m,weather_code
      ↓
   Returns: { current: { temperature_2m: 28.5, wind_speed_10m: 12.1, weather_code: 3 } }

3. Build SQS payload:
   {
     "tenantId":   "tenant-001",
     "hotelCode":  "H001",
     "executedAt": "2025-03-15T01:30:00Z",
     "weatherData": { ... }
   }

4. sqs.SendMessage() with MessageAttribute tenantId="tenant-001"
```

In the real system, step 2 would call the OHIP API for that tenant's hotel. Open-Meteo is used here as a free, keyless substitute to demonstrate the architecture.

### Scheduler package (`internal/scheduler/scheduler.go`)

Three exported functions:

| Function | When called | What it does |
|----------|------------|--------------|
| `Upsert(tenantID, timezone, runTime)` | Create or update tenant | UpdateSchedule → if 404, CreateSchedule |
| `Disable(tenantID)` | Set `enabled=false` | GetSchedule → UpdateSchedule with `State=DISABLED` |
| `Delete(tenantID)` | Delete tenant | DeleteSchedule (safe if already gone) |

The `Disable` function must call `GetSchedule` first because `UpdateSchedule` requires all fields (cron expression, target, etc.) to be re-supplied — it is not a partial update API.

### DB package (`internal/db/db.go`)

Uses `pgx/v5/stdlib` — the modern PostgreSQL driver for Go. The `database/sql` standard interface is used so the rest of the code has no pgx dependency.

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
│     - lambda:InvokeFunction → pms-trigger Lambda only          │
└─────────────────────────────────────────────────────────────────┘
```

### Why `iam:PassRole` is required

When the API Lambda calls `CreateSchedule` or `UpdateSchedule`, it must specify a `RoleArn` that EventBridge will assume when the schedule fires. AWS enforces that the caller (the API Lambda) has `iam:PassRole` on that specific role before it can hand it to another service. Without this, `CreateSchedule` returns an `AccessDenied` error.

---

## 8. Data Flow — Step by Step

### Flow A: Creating a new tenant

```
User fills in UI → "Hotel: Grand Mumbai, Run at 07:00, Timezone: Asia/Kolkata"
         │
         ▼
POST /tenants
{
  "tenantId":  "tenant-001",
  "name":      "Grand Hotel Mumbai",
  "timezone":  "Asia/Kolkata",
  "runTime":   "07:00",
  "latitude":  19.076,
  "longitude": 72.8777,
  "hotelCode": "H001"
}
         │
         ▼
API Lambda (api/main.go)
  ├── db.CreateTenant()
  │     INSERT INTO tenant_config VALUES (...)
  │
  └── sched.Upsert("tenant-001", "Asia/Kolkata", "07:00")
        ├── Try UpdateSchedule → 404 ResourceNotFoundException
        └── CreateSchedule
              Name:       "tenant-tenant-001"
              GroupName:  "pms-schedulers"
              Expression: "cron(0 7 * * ? *)"
              Timezone:   "Asia/Kolkata"
              Target:     pms-trigger Lambda
              Input:      {"tenantId":"tenant-001"}
         │
         ▼
Response: HTTP 201
{
  "tenantId": "tenant-001",
  "name": "Grand Hotel Mumbai",
  "timezone": "Asia/Kolkata",
  "runTime": "07:00",
  "enabled": true,
  ...
}
```

### Flow B: Tenant changes their schedule

```
User changes run time from 07:00 → 09:30
         │
         ▼
PUT /tenants/tenant-001/schedule
{ "timezone": "Asia/Kolkata", "runTime": "09:30", "enabled": true }
         │
         ▼
API Lambda
  ├── db.UpdateTenantSchedule("tenant-001", "Asia/Kolkata", "09:30", true)
  │     UPDATE tenant_config SET run_time='09:30', updated_at=NOW() WHERE tenant_id='tenant-001'
  │
  └── sched.Upsert("tenant-001", "Asia/Kolkata", "09:30")
        └── UpdateSchedule
              Expression: "cron(30 9 * * ? *)"   ← changed from cron(0 7 * * ? *)
              Timezone:   "Asia/Kolkata"          ← unchanged
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
EventBridge Scheduler "tenant-tenant-001" fires
         │  Assumes pms-scheduler-execution-role
         │  Invokes pms-trigger Lambda
         ▼
Trigger Lambda receives:
{ "tenantId": "tenant-001" }
         │
         ▼
db.GetTenant("tenant-001")
→ { hotelCode: "H001", latitude: 19.076, longitude: 72.877, ... }
         │
         ▼
GET https://api.open-meteo.com/v1/forecast
    ?latitude=19.076000&longitude=72.877700
    &current=temperature_2m,wind_speed_10m,weather_code
→ { current: { temperature_2m: 31.2, wind_speed_10m: 8.4, weather_code: 1 } }
         │
         ▼
Build payload:
{
  "tenantId":   "tenant-001",
  "hotelCode":  "H001",
  "executedAt": "2025-03-15T04:00:00Z",
  "weatherData": {
    "latitude":  19.076,
    "longitude": 72.877,
    "current": {
      "temperature_2m": 31.2,
      "wind_speed_10m": 8.4,
      "weather_code":   1
    }
  }
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
PUT /tenants/tenant-002/schedule
{ "timezone": "America/New_York", "runTime": "09:00", "enabled": false }
         │
         ▼
API Lambda
  ├── db.UpdateTenantSchedule(..., enabled=false)
  └── sched.Disable("tenant-002")
        ├── GetSchedule("tenant-tenant-002")
        │   → current expression, timezone, target
        └── UpdateSchedule
              State: DISABLED   ← scheduler still exists, just paused
```

### Flow E: Deleting a tenant

```
DELETE /tenants/tenant-002
         │
         ▼
API Lambda
  ├── db.DeleteTenant("tenant-002")       ← row gone from RDS
  └── sched.Delete("tenant-002")          ← scheduler gone from EventBridge
        └── DeleteSchedule("tenant-tenant-002")
              (no-op if already deleted)
```

---

## 9. API Reference

Base URL: `https://{api-id}.execute-api.{region}.amazonaws.com`

Get it after deployment: `terraform output api_endpoint`

---

### Create a tenant

```
POST /tenants
Content-Type: application/json
```

**Request body — daily schedule:**

```json
{
  "tenantId":     "tenant-001",
  "name":         "Grand Hotel Mumbai",
  "scheduleType": "daily",
  "timezone":     "Asia/Kolkata",
  "runTime":      "07:00",
  "latitude":     19.0760,
  "longitude":    72.8777,
  "hotelCode":    "H001"
}
```

**Request body — rate schedule:**

```json
{
  "tenantId":     "tenant-011",
  "name":         "Realtime Hotel",
  "scheduleType": "rate",
  "rateMinutes":  60,
  "latitude":     19.0760,
  "longitude":    72.8777,
  "hotelCode":    "H011"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `tenantId` | string | Yes | Unique ID, used as scheduler name suffix |
| `name` | string | No | Human-readable hotel name |
| `scheduleType` | string | No | `"daily"` (default) or `"rate"` |
| `timezone` | string | Yes (daily) | IANA timezone name (e.g., `Asia/Kolkata`) |
| `runTime` | string | Yes (daily) | Daily execution time in `HH:MM` 24-hour format |
| `rateMinutes` | int | Yes (rate) | Interval in minutes (e.g., `60` = every 1 hour) |
| `latitude` | float | No | Hotel coordinates — used to call Open-Meteo |
| `longitude` | float | No | Hotel coordinates — used to call Open-Meteo |
| `hotelCode` | string | No | Hotel code included in SQS payload |

**Response `201 Created`:**

```json
{
  "tenantId":  "tenant-001",
  "name":      "Grand Hotel Mumbai",
  "timezone":  "Asia/Kolkata",
  "runTime":   "07:00",
  "enabled":   true,
  "latitude":  19.076,
  "longitude": 72.8777,
  "hotelCode": "H001",
  "createdAt": "2025-03-15T10:00:00Z",
  "updatedAt": "2025-03-15T10:00:00Z"
}
```

**Side effect:** Creates EventBridge Scheduler `tenant-tenant-001` in group `pms-schedulers`.

---

### List all tenants

```
GET /tenants
```

**Response `200 OK`:** Array of tenant objects (same shape as above).

---

### Get a single tenant

```
GET /tenants/{tenantId}
```

**Response `200 OK`:** Single tenant object.
**Response `404 Not Found`:** `{ "error": "tenant not found" }`

---

### Update a tenant's schedule

```
PUT /tenants/{tenantId}/schedule
Content-Type: application/json
```

**Request body — switch to daily:**

```json
{ "scheduleType": "daily", "timezone": "America/Chicago", "runTime": "10:00", "enabled": true }
```

**Request body — switch to rate (every 1 hour):**

```json
{ "scheduleType": "rate", "rateMinutes": 60, "enabled": true }
```

**Request body — pause the scheduler:**

```json
{ "scheduleType": "daily", "timezone": "Asia/Kolkata", "runTime": "07:00", "enabled": false }
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `scheduleType` | string | No | `"daily"` (default) or `"rate"` |
| `timezone` | string | Yes (daily) | IANA timezone name |
| `runTime` | string | Yes (daily) | New time in `HH:MM` 24-hour format |
| `rateMinutes` | int | Yes (rate) | Interval in minutes (e.g., `60` = every 1 hour) |
| `enabled` | boolean | No | `false` pauses the scheduler without deleting it. Defaults to `true`. |

**Response `200 OK`:** `{ "status": "updated" }`

**Side effects:**
- `enabled: true` → calls `UpdateSchedule` with new expression (cron or rate)
- `enabled: false` → calls `UpdateSchedule` with `State: DISABLED`

---

### Delete a tenant

```
DELETE /tenants/{tenantId}
```

**Response `200 OK`:** `{ "status": "deleted" }`

**Side effects:** Deletes row from RDS and removes the EventBridge Scheduler.

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
Outputs to `/tmp/pms-build/` (WSL-native filesystem — avoids NTFS permission errors):
```
/tmp/pms-build/
├── api/
│   ├── bootstrap   (compiled Go binary)
│   └── api.zip
└── trigger/
    ├── bootstrap
    └── trigger.zip
```

**Windows PowerShell (native):**
```powershell
.\build.ps1
```
Outputs to `bin\` in the project directory:
```
bin\
├── api\
│   ├── bootstrap
│   └── api.zip
└── trigger\
    ├── bootstrap
    └── trigger.zip
```

---

### Step 3 — Deploy infrastructure

The deployment is a **three-phase process** because `filebase64sha256` reads the local zip at plan time and the Lambda resource pulls from S3 at apply time — the bucket and zips must exist before the Lambdas are created.

**Recommended — use `make deploy` (handles all three phases automatically):**
```bash
make deploy
```

`make deploy` runs in order:
1. `make build` — compiles and zips both Lambdas
2. `terraform apply -target=aws_s3_bucket.lambda_artifacts` — creates the S3 bucket first
3. `make upload` — uploads both zips to S3
4. `terraform apply` — creates all remaining resources (RDS, Lambda, API Gateway, …)

**Manual equivalent (first-time init only):**
```bash
cd terraform && terraform init    # download providers — only needed once
```
Then run `make deploy` from the project root.

Total deploy time is ~8–10 minutes, mostly waiting for RDS to become available.

After `apply`, note the outputs:

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

**Linux / macOS / Git Bash:**
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
✓ tenant-001 | Grand Hotel Mumbai              | 07:00 Asia/Kolkata
✓ tenant-002 | NYC Plaza Hotel                 | 09:00 America/New_York
✓ tenant-003 | London Bridge Hotel             | 18:00 Europe/London
✓ tenant-004 | Tokyo Imperial Hotel            | 06:00 Asia/Tokyo
✓ tenant-005 | Dubai Oasis Resort              | 08:00 Asia/Dubai
✓ tenant-006 | Sydney Harbour Hotel            | 10:00 Australia/Sydney
✓ tenant-007 | Paris Grand Hotel               | 20:00 Europe/Paris
✓ tenant-008 | Singapore Marina Hotel          | 11:00 Asia/Singapore
✓ tenant-009 | São Paulo Business Hotel        | 14:00 America/Sao_Paulo
✓ tenant-010 | Toronto Skyline Hotel           | 07:30 America/Toronto

Done. created=10 failed=0
```

---

### Step 5 — Verify schedulers were created

List all schedulers (name + state only — the AWS ListSchedules API does not return the expression or timezone fields):

```bash
aws scheduler list-schedules \
  --group-name pms-schedulers \
  --query "Schedules[*].{Name:Name,State:State}" \
  --output table
```

Expected output:
```
┌──────────────────────┬──────────────┐
│         Name         │    State     │
├──────────────────────┼──────────────┤
│ tenant-tenant-001    │ ENABLED      │
│ tenant-tenant-002    │ ENABLED      │
│ tenant-tenant-003    │ ENABLED      │
│ ...                  │ ENABLED      │
└──────────────────────┴──────────────┘
```

To see the cron expression and timezone for a specific scheduler, use `get-schedule`:

```bash
aws scheduler get-schedule \
  --group-name pms-schedulers \
  --name tenant-tenant-001 \
  --query "{Expression:ScheduleExpression,Timezone:ScheduleExpressionTimezone,State:State}"
```

Expected output:
```json
{
    "Expression": "cron(0 7 * * ? *)",
    "Timezone": "Asia/Kolkata",
    "State": "ENABLED"
}
```

---

### Updating Lambda code

After a code change, rebuild and redeploy:

```bash
make build         # recompile
make upload        # push new zips to S3
cd terraform && terraform apply -auto-approve   # Terraform detects changed source_code_hash
```

Or use the single command that does all three:
```bash
make deploy
```

---

### Teardown

```bash
cd terraform
terraform destroy
```

This removes all AWS resources. RDS data is lost (`skip_final_snapshot = true`).

---

## 11. Testing & Verification

### Manual trigger — fire a Lambda immediately

Invoke the trigger Lambda directly without waiting for the scheduler:

```bash
aws lambda invoke \
  --function-name pms-trigger \
  --payload '{"tenantId":"tenant-001"}' \
  --cli-binary-format raw-in-base64-out \
  response.json

cat response.json
# null means success; an error string means failure
```

### Check CloudWatch logs

```bash
# API Lambda logs
aws logs tail /aws/lambda/pms-api --follow

# Trigger Lambda logs
aws logs tail /aws/lambda/pms-trigger --follow
```

After a manual invocation you should see (logs are JSON — one object per line):

```json
{"time":"2026-06-16T01:30:00Z","level":"INFO","msg":"trigger started","tenantId":"tenant-001"}
{"time":"2026-06-16T01:30:00Z","level":"INFO","msg":"db: tenant loaded","tenantId":"tenant-001","name":"Grand Hotel Mumbai","hotelCode":"H001","timezone":"Asia/Kolkata","scheduledAt":"07:00","lat":19.076,"lon":72.8777}
{"time":"2026-06-16T01:30:00Z","level":"INFO","msg":"api: weather fetched","tenantId":"tenant-001","hotelCode":"H001","localTime":"2026-06-16T07:00","temperature":"31.2°C","windSpeed":"8.4 km/h","weatherCode":1}
{"time":"2026-06-16T01:30:01Z","level":"INFO","msg":"trigger complete","tenantId":"tenant-001","hotelCode":"H001","sqsMessageId":"abc-12345-xyz","durationMs":450}
```

To pretty-print, pipe through `jq`:
```bash
aws logs tail /aws/lambda/pms-trigger --follow | jq .
```

### Read messages from SQS

```bash
# Receive one message (does not delete it)
# Run from the project root — cd terraform is needed for terraform output
aws sqs receive-message \
  --queue-url $(cd terraform && terraform output -raw sqs_queue_url) \
  --message-attribute-names tenantId \
  --query "Messages[0].Body" \
  --output text | python3 -m json.tool
```

Expected output:

```json
{
  "tenantId": "tenant-001",
  "hotelCode": "H001",
  "executedAt": "2025-03-15T01:30:00Z",
  "weatherData": {
    "latitude": 19.076,
    "longitude": 72.8777,
    "timezone": "Asia/Kolkata",
    "current": {
      "time": "2025-03-15T07:00",
      "temperature_2m": 31.2,
      "wind_speed_10m": 8.4,
      "weather_code": 1
    }
  }
}
```

### Verify schedule update takes effect immediately

```bash
# Change tenant-001 to run at 09:30 instead of 07:00
curl -X PUT \
  https://<api-endpoint>/tenants/tenant-001/schedule \
  -H "Content-Type: application/json" \
  -d '{"timezone":"Asia/Kolkata","runTime":"09:30","enabled":true}'

# Verify the scheduler was updated
aws scheduler get-schedule \
  --group-name pms-schedulers \
  --name tenant-tenant-001 \
  --query "{Expression:ScheduleExpression,Timezone:ScheduleExpressionTimezone}"
```

Expected: `{ "Expression": "cron(30 9 * * ? *)", "Timezone": "Asia/Kolkata" }`

---

## 12. Operational Topics

### Scheduler recovery

If schedulers are accidentally deleted:

```bash
# 1. List all enabled tenants from the database
psql "postgresql://pmsadmin:<pw>@<rds-endpoint>:5432/pmsdb" \
  -c "SELECT tenant_id, timezone, run_time FROM tenant_config WHERE enabled = true;"

# 2. Re-POST each tenant through the API to recreate their scheduler
# (POST is idempotent for scheduler creation — Upsert tries Update first)
curl -X POST https://<api-endpoint>/tenants/tenant-001/schedule \
  -H "Content-Type: application/json" \
  -d '{"timezone":"Asia/Kolkata","runTime":"07:00","enabled":true}'
```

Or write a recovery script that reads all rows and calls `PUT /tenants/{id}/schedule` for each one.

### Dead letter queue monitoring

If the Trigger Lambda fails 3 times for a message, it goes to the DLQ. Monitor it:

```bash
aws sqs get-queue-attributes \
  --queue-url $(terraform output -raw dlq_url) \
  --attribute-names ApproximateNumberOfMessages
```

Common failure reasons:
- RDS unreachable (security group issue or RDS stopped)
- Open-Meteo API returned an unexpected response format
- SQS permissions error
- Lambda timeout (default 60s)

### Disabling a tenant temporarily

```bash
# Pause — scheduler remains but does not fire
curl -X PUT https://<api-endpoint>/tenants/tenant-003/schedule \
  -d '{"timezone":"Europe/London","runTime":"18:00","enabled":false}'

# Resume
curl -X PUT https://<api-endpoint>/tenants/tenant-003/schedule \
  -d '{"timezone":"Europe/London","runTime":"18:00","enabled":true}'
```

### Scaling to hundreds of tenants

Each tenant is one EventBridge Scheduler. AWS supports up to 1,000,000 schedulers per account. The system scales linearly:

- 10 tenants → 10 schedulers, 10 Lambda invocations/day
- 500 tenants → 500 schedulers, 500 Lambda invocations/day
- No code changes required

The only bottleneck is RDS connection count. At very high scale, replace the direct RDS connection in Lambdas with **RDS Proxy** (a managed connection pooler).

---

## 13. Pricing Estimate

All prices are **us-east-1, on-demand, as of 2025**. Assumes 30-day month.

### Infrastructure in this deployment

| Component | Spec | Monthly (fixed) |
|---|---|---|
| RDS db.t3.micro | PostgreSQL 15, 20 GB gp2, single-AZ | $14.71 |
| S3 (Lambda artifacts) | ~5 MB total | < $0.01 |
| API Gateway HTTP v2 | management calls only (~500/month) | < $0.01 |
| **Fixed subtotal** | | **~$14.72** |

RDS breakdown: instance $0.017/hr × 730 hr = **$12.41** + storage 20 GB × $0.115 = **$2.30**.

---

### Variable costs per invocation

One Trigger Lambda invocation happens every time a scheduler fires.
Each invocation: DB query → Open-Meteo HTTP call → SQS send → ~500 ms average.

| Service | Price | Permanent free tier |
|---|---|---|
| EventBridge Scheduler | $1.00 per 1 M invocations | — |
| Lambda requests | $0.20 per 1 M requests | **1 M/month free** |
| Lambda duration (256 MB) | $0.0000166667 /GB-s → **$0.0000041667 /s** | **400 K GB-s/month free** |
| SQS | $0.40 per 1 M messages | **1 M/month free** |
| CloudWatch Logs | $0.50 per GB ingested | first 5 GB/month free |

---

### Monthly invocations

| Scenario | Invocations/day | Invocations/month |
|---|---|---|
| 100 tenants — daily | 100 | **3,000** |
| 200 tenants — daily | 200 | **6,000** |
| 100 tenants — hourly (`rate(1 hour)`) | 2,400 | **72,000** |
| 200 tenants — hourly (`rate(1 hour)`) | 4,800 | **144,000** |

---

### Cost breakdown per scenario

#### 100 tenants — daily (3,000 invocations/month)

| Line item | Calculation | Cost |
|---|---|---|
| RDS | fixed | $14.71 |
| EventBridge Scheduler | 3,000 / 1 M × $1.00 | $0.003 |
| Lambda requests | 3,000 → within 1 M free tier | $0.00 |
| Lambda duration | 3,000 × 0.5 s × 0.25 GB = 375 GB-s → within 400 K free | $0.00 |
| SQS | 3,000 → within 1 M free tier | $0.00 |
| CloudWatch Logs | 3,000 × 1 KB = 3 MB | $0.002 |
| **Total** | | **~$14.72 / month** |

#### 200 tenants — daily (6,000 invocations/month)

| Line item | Calculation | Cost |
|---|---|---|
| RDS | fixed | $14.71 |
| EventBridge Scheduler | 6,000 / 1 M × $1.00 | $0.006 |
| Lambda requests | within free tier | $0.00 |
| Lambda duration | 6,000 × 0.5 × 0.25 = 750 GB-s → within free tier | $0.00 |
| SQS | within free tier | $0.00 |
| CloudWatch Logs | 6 MB | $0.003 |
| **Total** | | **~$14.72 / month** |

#### 100 tenants — hourly (72,000 invocations/month)

| Line item | Calculation | Cost |
|---|---|---|
| RDS | fixed | $14.71 |
| EventBridge Scheduler | 72,000 / 1 M × $1.00 | $0.072 |
| Lambda requests | 72,000 → within 1 M free tier | $0.00 |
| Lambda duration | 72,000 × 0.5 × 0.25 = 9,000 GB-s → within 400 K free | $0.00 |
| SQS | 72,000 → within 1 M free tier | $0.00 |
| CloudWatch Logs | 72 MB | $0.036 |
| **Total** | | **~$14.82 / month** |

#### 200 tenants — hourly (144,000 invocations/month)

| Line item | Calculation | Cost |
|---|---|---|
| RDS | fixed | $14.71 |
| EventBridge Scheduler | 144,000 / 1 M × $1.00 | $0.144 |
| Lambda requests | 144,000 → within 1 M free tier | $0.00 |
| Lambda duration | 144,000 × 0.5 × 0.25 = 18,000 GB-s → within 400 K free | $0.00 |
| SQS | 144,000 → within 1 M free tier | $0.00 |
| CloudWatch Logs | 144 MB | $0.072 |
| **Total** | | **~$14.93 / month** |

---

### Summary

| Scenario | Monthly cost |
|---|---|
| 100 tenants, daily | **~$14.72** |
| 200 tenants, daily | **~$14.72** |
| 100 tenants, hourly | **~$14.82** |
| 200 tenants, hourly | **~$14.93** |

---

### Why the cost barely changes

**Lambda and SQS are essentially free** at this scale due to their permanent free tiers:
- Lambda free tier covers up to ~3.2 million invocations/month at 256 MB/500 ms each before paying anything.
- SQS free tier covers 1 million messages/month — 200 hourly tenants send only 144,000.
- Even 1,000 tenants running hourly (720,000 invocations/month) stays within both free tiers.

**RDS is the dominant cost** — it runs 24/7 regardless of how many tenants you have or how often they fire. Going from 10 tenants to 1,000 adds less than $1/month in variable costs; the instance is always ~$14.71.

**Switching from daily to hourly adds ~$0.21/month** for 200 tenants — the only meaningful variable line items are EventBridge Scheduler and CloudWatch log ingestion.

---

### Free tier notes

| Service | Free tier type | Limit |
|---|---|---|
| Lambda requests | **Permanent** (all accounts) | 1 M requests/month |
| Lambda duration | **Permanent** (all accounts) | 400,000 GB-seconds/month |
| SQS | **Permanent** (all accounts) | 1 M requests/month |
| CloudWatch Logs storage | **Permanent** | 5 GB/month |
| RDS | First 12 months only (new accounts) | 750 hr/month of db.t3.micro |

New AWS accounts get the RDS instance **free for 12 months**, making the total cost $0.05–$0.22/month during that window.

### Production hardening checklist

| Item | Current (dev) | Production recommendation |
|------|--------------|--------------------------|
| RDS access | Publicly accessible | Private subnet + RDS Proxy |
| RDS security group | Open to 0.0.0.0/0 | Restrict to Lambda security group only |
| DB credentials | Terraform variable | AWS Secrets Manager + automatic rotation |
| Terraform state | Local | S3 backend + DynamoDB locking |
| Lambda in VPC | No | Yes — same VPC as RDS |
| API authentication | None | API Gateway authorizer (JWT/Cognito/IAM) |
| RDS snapshot | `skip_final_snapshot = true` | `skip_final_snapshot = false` |
| RDS deletion protection | `false` | `true` |
