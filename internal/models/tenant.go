package models

import (
	"encoding/json"
	"time"
)

type Tenant struct {
	TenantID    string          `json:"tenantId"`
	PMSProvider string          `json:"pmsProvider"`
	Config      json.RawMessage `json:"config,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
}

// ScheduleInfo is fetched live from EventBridge — never stored in the DB.
type ScheduleInfo struct {
	Expression string `json:"expression"`
	Timezone   string `json:"timezone"`
	State      string `json:"state"` // "ENABLED" or "DISABLED"
}

type TenantWithSchedule struct {
	TenantID    string          `json:"tenantId"`
	PMSProvider string          `json:"pmsProvider"`
	Config      json.RawMessage `json:"config,omitempty"`
	Schedule    *ScheduleInfo   `json:"schedule,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
}

type CreateTenantRequest struct {
	TenantID    string          `json:"tenantId"`
	PMSProvider string          `json:"pmsProvider"`
	Config      json.RawMessage `json:"config,omitempty"`
	Expression  string          `json:"expression"`
	Timezone    string          `json:"timezone"`
	Enabled     bool            `json:"enabled"`
}

type UpdateScheduleRequest struct {
	Expression string `json:"expression"`
	Timezone   string `json:"timezone"`
	Enabled    bool   `json:"enabled"`
}

type UpdateConfigRequest struct {
	Config json.RawMessage `json:"config"`
}
