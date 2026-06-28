# Multi-Tenant PMS Scheduler — Full Architecture Diagram

```mermaid
flowchart TD
    classDef client    fill:#4A90D9,stroke:#2C5F8A,color:#fff
    classDef apigw     fill:#E8761A,stroke:#B35A13,color:#fff
    classDef lambda    fill:#FF9900,stroke:#CC7A00,color:#000
    classDef db        fill:#3B48CC,stroke:#2A3599,color:#fff
    classDef eb        fill:#E7157B,stroke:#B50F5F,color:#fff
    classDef sqs       fill:#FF4F8B,stroke:#CC3A6E,color:#fff
    classDef iam       fill:#DD344C,stroke:#A82438,color:#fff
    classDef cw        fill:#7B42BC,stroke:#5C3190,color:#fff
    classDef s3        fill:#3F8624,stroke:#2D601A,color:#fff

    %% ── CLIENT ────────────────────────────────────────────────────────────────
    CLIENT(["Client\ncurl / PMS UI / HTTP tool"]):::client

    %% ── API GATEWAY ───────────────────────────────────────────────────────────
    subgraph APIGW["API Gateway HTTP v2"]
        direction TB
        AG1["POST   /tenants"]
        AG2["GET    /tenants"]
        AG3["GET    /tenants/{id}"]
        AG4["PUT    /tenants/{id}/config"]
        AG5["DELETE /tenants/{id}"]
        AG6["GET    /health"]
    end

    CLIENT -->|"HTTPS request"| APIGW

    %% ── API LAMBDA ────────────────────────────────────────────────────────────
    subgraph APIL["API Lambda — pms-api  ·  256 MB  ·  30 s timeout  ·  provided.al2023"]
        direction TB

        subgraph AINIT["init()  — cold start only"]
            AI1["db.Connect()\nsql.Open pgx driver\nconn.Ping()"]
            AI2["db.Migrate()\nDetects old schema via tenant table existence\nDrops legacy tenant_config if needed\nCREATE TABLE IF NOT EXISTS tenant\nCREATE TABLE IF NOT EXISTS tenant_config"]
            AI1 --> AI2
        end

        subgraph AHAND["handler(ctx, APIGatewayV2HTTPRequest)"]
            direction TB
            AV["validateConfig(config)\nrequire config.name + config.timezone + config.cron"]

            AR1["POST /tenants\nsched.Upsert(config.name, config.timezone, config.cron, tenantID, enabled)\ndb.CreateTenant(tenantID, pmsProvider, config)  [tx]\n  INSERT INTO tenant\n  INSERT INTO tenant_config"]

            AR2["GET /tenants\ndb.ListTenants(ctx)\nSELECT t.*, tc.config FROM tenant t JOIN tenant_config tc"]

            AR3["GET /tenants/{id}\ndb.GetTenant(ctx, tenantID)\nSELECT t.*, tc.config FROM tenant t JOIN tenant_config tc"]

            AR4["PUT /tenants/{id}/config\ndb.GetTenant() → fetch old config.name\nsched.Upsert(new config)\nif name changed → sched.Delete(oldName)\ndb.UpdateTenantConfig(tenantID, config)"]

            AR5["DELETE /tenants/{id}\ndb.GetTenant() → fetch config.name\ndb.DeleteTenant()  ← cascades to tenant_config\nsched.Delete(config.name)"]

            AR6["GET /health\ndb.Ping(ctx)\n200 → {status: ok}\n503 → {error: database unavailable}"]

            AV --> AR1 & AR4
        end
    end

    APIGW -->|"Lambda Proxy\nIntegration"| APIL

    %% ── SCHEDULER PACKAGE ─────────────────────────────────────────────────────
    subgraph SCHPKG["internal/scheduler  —  Scheduler Package"]
        direction TB

        SU["Upsert(name, timezone, cronExpr, tenantID, enabled)\n───────────────────────────────────────────────\nstate = ENABLED if enabled else DISABLED\npayload = {tenantId: tenantID}\n1. UpdateSchedule(Name, GroupName, Expression, Timezone, State, Target)\n   success  → return nil\n   ResourceNotFoundException  → fall through\n2. CreateSchedule(same params)"]

        SDEL["Delete(name)\n───────────────────────────────────────────────\nDeleteSchedule(Name, GroupName)\nResourceNotFoundException  → no-op  (idempotent)"]
    end

    APIL -->|"Upsert / Delete\ncalls"| SCHPKG

    %% ── RDS ───────────────────────────────────────────────────────────────────
    subgraph RDSBOX["RDS PostgreSQL 15  —  db.t3.micro  ·  20 GB gp2  ·  single-AZ"]
        direction TB
        TENANT[("tenant\n══════════════════════════════\ntenant_id     VARCHAR(50)   PK\npms_provider  VARCHAR(100)  NOT NULL\ncreated_at    TIMESTAMPTZ")]:::db

        TCONFIG[("tenant_config\n══════════════════════════════\ntenant_id  VARCHAR(50)   PK → tenant(tenant_id)\nconfig     JSONB  NOT NULL\n  {\n    name:     string,\n    timezone: string,\n    cron:     string,\n    enabled:  bool\n  }\ncreated_at TIMESTAMPTZ\nupdated_at TIMESTAMPTZ")]:::db

        TENANT -->|"ON DELETE CASCADE"| TCONFIG
    end

    APIL  -->|"CreateTenant [tx]\nListTenants\nGetTenant\nUpdateTenantConfig\nDeleteTenant\nPing"| RDSBOX

    %% ── IAM ───────────────────────────────────────────────────────────────────
    subgraph IAMBOX["IAM Roles"]
        direction TB
        IAM1["pms-api-lambda-role\nAssumed by: lambda.amazonaws.com\n──────────────────────\nscheduler:CreateSchedule\nscheduler:UpdateSchedule\nscheduler:GetSchedule\nscheduler:DeleteSchedule\nscheduler:ListSchedules\niam:PassRole → execution-role only\nlogs:*"]:::iam

        IAM2["pms-scheduler-execution-role\nAssumed by: scheduler.amazonaws.com\n──────────────────────\nlambda:InvokeFunction\n→ pms-trigger ARN only"]:::iam

        IAM3["pms-trigger-lambda-role\nAssumed by: lambda.amazonaws.com\n──────────────────────\nsqs:SendMessage → pms-queue ARN only\nsqs:GetQueueAttributes\nlogs:*"]:::iam
    end

    APIL  -. "assumes\npms-api-lambda-role" .-> IAM1
    SCHPKG -->|"iam:PassRole required\nto hand role to EventBridge\nwhen calling CreateSchedule"| IAM2

    %% ── EVENTBRIDGE ───────────────────────────────────────────────────────────
    subgraph EBGROUP["EventBridge Scheduler Group — pms-schedulers"]
        direction TB
        EB1["tenant-001-run\nExpression : cron(0 7 * * ? *)\nTimezone   : Asia/Kolkata  ← DST-aware\nState      : ENABLED\nTarget ARN : pms-trigger Lambda\nInput      : {tenantId: 'tenant-001'}"]:::eb

        EB2["tenant-002-run\nExpression : cron(0 9 * * ? *)\nTimezone   : America/New_York  ← DST-aware\nState      : ENABLED\nInput      : {tenantId: 'tenant-002'}"]:::eb

        EB3["tenant-NNN-run\nExpression : rate(1 hour)\nTimezone   : UTC\nState      : DISABLED  ← enabled=false\nInput      : {tenantId: 'tenant-NNN'}"]:::eb
    end

    SCHPKG -->|"CreateSchedule\nUpdateSchedule\nDeleteSchedule\nvia AWS SDK"| EBGROUP

    %% ── TRIGGER LAMBDA ────────────────────────────────────────────────────────
    subgraph TRIGL["Trigger Lambda — pms-trigger  ·  256 MB  ·  60 s timeout  ·  provided.al2023"]
        direction TB

        subgraph TINIT["init()  — cold start only"]
            TI1["db.Connect()"]
            TI2["config.LoadDefaultConfig()\nsqs.NewFromConfig(cfg)\n→ sqsClient (package-level var)"]
            TI1 --> TI2
        end

        subgraph THAND["handler(ctx, TriggerEvent{TenantID string})"]
            direction TB
            T1["Step 1 — db.GetTenant(ctx, event.TenantID)\nSELECT t.*, tc.config\nFROM tenant t JOIN tenant_config tc\nreturns pmsProvider + config"]

            T2["Step 2 — sqsClient.SendMessage(ctx, input)\nQueueUrl  : SQS_QUEUE_URL env var\nBody      : JSON{tenantId, pmsProvider, executedAt}\nMessageAttribute: tenantId (String)"]

            T1 --> T2
        end
    end

    EBGROUP -->|"Fires at scheduled time\nAssumes pms-scheduler-execution-role\nInput: {tenantId: 'tenant-NNN'}"| TRIGL
    IAM2 -. "grants\nlambda:InvokeFunction" .-> TRIGL

    TRIGL -->|"db.GetTenant\nvia pgx/v5 connection pool"| RDSBOX

    %% ── SQS ───────────────────────────────────────────────────────────────────
    subgraph SQSBOX["Amazon SQS"]
        direction TB
        SQSQ["pms-queue  (Standard Queue)\nMessage body: JSON payload\n{tenantId, pmsProvider, executedAt}\nMessageAttribute: tenantId"]:::sqs

        SQSDLQ["pms-dlq  (Dead Letter Queue)\nReceives messages after\nmaxReceiveCount = 3 failed deliveries"]:::sqs

        SQSQ -->|"3 failed\ndeliveries"| SQSDLQ
    end

    TRIGL  -->|"sqsClient.SendMessage()\nIAM: sqs:SendMessage"| SQSQ
    IAM3 -. "grants\nsqs:SendMessage" .-> SQSQ

    %% ── CONSUMERS ─────────────────────────────────────────────────────────────
    CONSUMERS(["Downstream Consumers\nAny SQS subscriber"]):::client
    SQSQ -->|"SQS long-poll\nor event-source mapping"| CONSUMERS

    %% ── CLOUDWATCH ────────────────────────────────────────────────────────────
    subgraph CWBOX["CloudWatch Logs"]
        direction LR
        CW1["/aws/lambda/pms-api\nretention: 7 days  ·  JSON slog\nkey fields: requestId, method, path, status, durationMs"]:::cw
        CW2["/aws/lambda/pms-trigger\nretention: 7 days  ·  JSON slog\nkey fields: tenantId, pmsProvider, sqsMessageId, durationMs"]:::cw
    end

    APIL  -->|"JSON structured logs"| CW1
    TRIGL -->|"JSON structured logs"| CW2

    %% ── S3 ────────────────────────────────────────────────────────────────────
    S3BUCK["S3 Bucket — pms-lambda-{accountId}\napi.zip  ·  trigger.zip\nUploaded by: make upload\nsource_code_hash for change detection"]:::s3

    S3BUCK -->|"s3_bucket + s3_key"| APIL
    S3BUCK -->|"s3_bucket + s3_key"| TRIGL
```

---

## Component Reference

### Client
Any HTTP client — curl, Postman, or a PMS front-end. Sends requests to the API Gateway base URL from `terraform output api_endpoint`.

---

### API Gateway HTTP v2
Lambda proxy integration — forwards the full request to the API Lambda and returns its response. No routing logic lives in API Gateway.

---

### API Lambda (`pms-api`)

**`init()` — runs once per cold start:**
- `db.Connect()` — opens PostgreSQL connection via `pgx/v5/stdlib`.
- `db.Migrate()` — detects old schema by checking for the `tenant` table. If absent, drops the legacy `tenant_config` table and creates both new tables. Idempotent on subsequent cold starts.

**`handler(ctx, req)` — runs on every request:**
- Parses method + path, extracts `tenantID` from path segments.
- `validateConfig()` — checks `config.name`, `config.timezone`, and `config.cron` are present.
- For create/update: calls EventBridge **first**, then writes to DB. Rolls back the scheduler on DB failure.
- For delete: fetches `config.name` from DB (to know the scheduler name), then deletes DB row (cascades), then deletes scheduler.

---

### internal/scheduler Package

**`Upsert(name, timezone, cronExpr, tenantID, enabled)`** — the hot-path write.
- Tries `UpdateSchedule` first (no existence check needed).
- Falls back to `CreateSchedule` only on `ResourceNotFoundException`.
- `enabled=false` sets `State=DISABLED` — the scheduler exists but won't fire.
- The cron expression is passed through as-is — no conversion in application code.

**`Delete(name)`** — calls `DeleteSchedule` by the scheduler's name. Ignores `ResourceNotFoundException` (idempotent).

---

### RDS PostgreSQL (`tenant` + `tenant_config`)

Two tables:
- `tenant` — one row per tenant: `tenant_id`, `pms_provider`.
- `tenant_config` — one JSONB config per tenant, foreign-keyed to `tenant` with `ON DELETE CASCADE`.

The `config` JSONB stores `{ name, timezone, cron, enabled }`. `config.name` is the EventBridge scheduler name.

Schedulers are derived resources — they can be deleted and recreated from these tables at any time without data loss.

---

### IAM Roles

**`pms-api-lambda-role`** — used by the API Lambda. Has `iam:PassRole` scoped to `pms-scheduler-execution-role` only. Required so the API Lambda can hand EventBridge a role ARN when calling `CreateSchedule`.

**`pms-scheduler-execution-role`** — assumed by EventBridge Scheduler. Grants only `lambda:InvokeFunction` on the Trigger Lambda ARN.

**`pms-trigger-lambda-role`** — used by the Trigger Lambda. Grants `sqs:SendMessage` scoped to `pms-queue` ARN only.

---

### EventBridge Scheduler Group (`pms-schedulers`)

One scheduler per tenant. Each stores:
- Scheduler name = `config.name` (e.g. `"tenant-001-run"`)
- The cron/rate expression from `config.cron`
- The timezone from `config.timezone` (AWS handles DST automatically)
- The payload `{"tenantId":"tenant-001"}` sent to the Trigger Lambda
- `State: ENABLED` or `DISABLED` based on `config.enabled`

---

### Trigger Lambda (`pms-trigger`)

Receives `{"tenantId":"tenant-001"}` from EventBridge.

1. `db.GetTenant(tenantID)` — loads `pmsProvider` and `config` from the joined tables.
2. Builds SQS payload: `{ tenantId, pmsProvider, executedAt }`.
3. `sqsClient.SendMessage()` — pushes to `pms-queue` with a `tenantId` MessageAttribute for downstream filtering.

---

### Amazon SQS (`pms-queue` + `pms-dlq`)

`pms-queue` buffers trigger output for async downstream processing. `pms-dlq` receives messages that fail 3 deliveries. Monitor `ApproximateNumberOfMessages` on the DLQ — any value above 0 means a tenant's job needs investigation.

---

### CloudWatch Logs

Both Lambdas use Go's `log/slog` with `JSONHandler`. The API Lambda tags every line with `requestId`; the Trigger Lambda tags every line with `tenantId`.

```
Filter API logs for one request:   { $.requestId = "abc-123" }
Filter trigger logs for one tenant: { $.tenantId = "tenant-001" }
```
