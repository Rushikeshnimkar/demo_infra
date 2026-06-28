// Seed creates sample tenants by calling the live API.
// Usage: API_URL=https://your-api.execute-api.us-east-1.amazonaws.com go run ./scripts/seed
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

type scheduleConfig struct {
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
	Cron     string `json:"cron"`
	Enabled  bool   `json:"enabled"`
}

type tenantReq struct {
	TenantID    string         `json:"tenantId"`
	PMSProvider string         `json:"pmsProvider"`
	Config      scheduleConfig `json:"config"`
}

var tenants = []tenantReq{
	{"tenant-001", "Opera",   scheduleConfig{"tenant-001-run", "Asia/Kolkata",      "cron(0 7  * * ? *)", true}},
	{"tenant-002", "Mews",    scheduleConfig{"tenant-002-run", "America/New_York",  "cron(0 9  * * ? *)", true}},
	{"tenant-003", "Apaleo",  scheduleConfig{"tenant-003-run", "Europe/London",     "cron(0 18 * * ? *)", true}},
	{"tenant-004", "Opera",   scheduleConfig{"tenant-004-run", "Asia/Tokyo",        "cron(0 6  * * ? *)", true}},
	{"tenant-005", "Mews",    scheduleConfig{"tenant-005-run", "Asia/Dubai",        "cron(0 8  * * ? *)", true}},
	{"tenant-006", "Apaleo",  scheduleConfig{"tenant-006-run", "Australia/Sydney",  "cron(0 10 * * ? *)", true}},
	{"tenant-007", "Opera",   scheduleConfig{"tenant-007-run", "Europe/Paris",      "cron(0 20 * * ? *)", true}},
	{"tenant-008", "Mews",    scheduleConfig{"tenant-008-run", "Asia/Singapore",    "cron(0 11 * * ? *)", true}},
	{"tenant-009", "Apaleo",  scheduleConfig{"tenant-009-run", "America/Sao_Paulo", "cron(0 14 * * ? *)", true}},
	{"tenant-010", "Opera",   scheduleConfig{"tenant-010-run", "America/Toronto",   "cron(30 7 * * ? *)", true}},
}

func main() {
	apiURL := os.Getenv("API_URL")
	if apiURL == "" {
		log.Fatal("API_URL environment variable is required.\n" +
			"Example: API_URL=https://abc123.execute-api.us-east-1.amazonaws.com go run ./scripts/seed")
	}

	created, failed := 0, 0
	for _, t := range tenants {
		body, _ := json.Marshal(t)
		resp, err := http.Post(apiURL+"/tenants", "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("ERROR %s: %v", t.TenantID, err)
			failed++
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 201 {
			fmt.Printf("✓ %s | %-12s | %s | %s\n", t.TenantID, t.PMSProvider, t.Config.Cron, t.Config.Timezone)
			created++
		} else {
			fmt.Printf("✗ %s | HTTP %d: %s\n", t.TenantID, resp.StatusCode, string(respBody))
			failed++
		}
	}
	fmt.Printf("\nDone. created=%d failed=%d\n", created, failed)
}
