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

type tenantReq struct {
	TenantID    string `json:"tenantId"`
	PMSProvider string `json:"pmsProvider"`
	Expression  string `json:"expression"`
	Timezone    string `json:"timezone"`
	Enabled     bool   `json:"enabled"`
}

var tenants = []tenantReq{
	{"tenant-001", "Opera",  "cron(0 7  * * ? *)", "Asia/Kolkata",      true},
	{"tenant-002", "Mews",   "cron(0 9  * * ? *)", "America/New_York",  true},
	{"tenant-003", "Apaleo", "cron(0 18 * * ? *)", "Europe/London",     true},
	{"tenant-004", "Opera",  "cron(0 6  * * ? *)", "Asia/Tokyo",        true},
	{"tenant-005", "Mews",   "cron(0 8  * * ? *)", "Asia/Dubai",        true},
	{"tenant-006", "Apaleo", "cron(0 10 * * ? *)", "Australia/Sydney",  true},
	{"tenant-007", "Opera",  "cron(0 20 * * ? *)", "Europe/Paris",      true},
	{"tenant-008", "Mews",   "cron(0 11 * * ? *)", "Asia/Singapore",    true},
	{"tenant-009", "Apaleo", "cron(0 14 * * ? *)", "America/Sao_Paulo", true},
	{"tenant-010", "Opera",  "cron(30 7 * * ? *)", "America/Toronto",   true},
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
			fmt.Printf("✓ %s | %-12s | %s | %s\n", t.TenantID, t.PMSProvider, t.Expression, t.Timezone)
			created++
		} else {
			fmt.Printf("✗ %s | HTTP %d: %s\n", t.TenantID, resp.StatusCode, string(respBody))
			failed++
		}
	}
	fmt.Printf("\nDone. created=%d failed=%d\n", created, failed)
}
