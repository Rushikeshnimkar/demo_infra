package models

import "time"

type Tenant struct {
	TenantID     string    `json:"tenantId"`
	Name         string    `json:"name"`
	ScheduleType string    `json:"scheduleType"` // "daily" or "rate"
	Timezone     string    `json:"timezone"`
	RunTime      string    `json:"runTime"`
	RateMinutes  int       `json:"rateMinutes"`
	Enabled      bool      `json:"enabled"`
	Latitude     float64   `json:"latitude"`
	Longitude    float64   `json:"longitude"`
	HotelCode    string    `json:"hotelCode"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type CreateTenantRequest struct {
	TenantID     string  `json:"tenantId"`
	Name         string  `json:"name"`
	ScheduleType string  `json:"scheduleType"` // "daily" or "rate"; defaults to "daily"
	Timezone     string  `json:"timezone"`      // required for daily
	RunTime      string  `json:"runTime"`       // required for daily (HH:MM 24-hour)
	RateMinutes  int     `json:"rateMinutes"`   // required for rate (e.g. 60 = every 1 hour)
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	HotelCode    string  `json:"hotelCode"`
}

type UpdateScheduleRequest struct {
	ScheduleType string `json:"scheduleType"` // "daily" or "rate"; defaults to "daily"
	Timezone     string `json:"timezone"`      // required for daily
	RunTime      string `json:"runTime"`       // required for daily (HH:MM 24-hour)
	RateMinutes  int    `json:"rateMinutes"`   // required for rate
	Enabled      *bool  `json:"enabled"`
}
