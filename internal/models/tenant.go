package models

import "time"

type ScheduleConfig struct {
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
	Cron     string `json:"cron"`
	Enabled  bool   `json:"enabled"`
}

type TenantWithConfig struct {
	TenantID    string         `json:"tenantId"`
	PMSProvider string         `json:"pmsProvider"`
	Config      ScheduleConfig `json:"config"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

type CreateTenantRequest struct {
	TenantID    string         `json:"tenantId"`
	PMSProvider string         `json:"pmsProvider"`
	Config      ScheduleConfig `json:"config"`
}

type UpdateConfigRequest struct {
	Config ScheduleConfig `json:"config"`
}
