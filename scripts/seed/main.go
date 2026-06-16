// Seed creates 10 sample tenants by calling the live API.
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
	TenantID  string  `json:"tenantId"`
	Name      string  `json:"name"`
	Timezone  string  `json:"timezone"`
	RunTime   string  `json:"runTime"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	HotelCode string  `json:"hotelCode"`
}

var tenants = []tenantReq{
	{"tenant-001", "Grand Hotel Mumbai",       "Asia/Kolkata",      "07:00",  19.0760,   72.8777,  "H001"},
	{"tenant-002", "NYC Plaza Hotel",          "America/New_York",  "09:00",  40.7580,  -73.9855,  "H002"},
	{"tenant-003", "London Bridge Hotel",      "Europe/London",     "18:00",  51.5080,   -0.1281,  "H003"},
	{"tenant-004", "Tokyo Imperial Hotel",     "Asia/Tokyo",        "06:00",  35.6895,  139.6917,  "H004"},
	{"tenant-005", "Dubai Oasis Resort",       "Asia/Dubai",        "08:00",  25.2048,   55.2708,  "H005"},
	{"tenant-006", "Sydney Harbour Hotel",     "Australia/Sydney",  "10:00", -33.8688,  151.2093,  "H006"},
	{"tenant-007", "Paris Grand Hotel",        "Europe/Paris",      "20:00",  48.8566,    2.3522,  "H007"},
	{"tenant-008", "Singapore Marina Hotel",   "Asia/Singapore",    "11:00",   1.2904,  103.8519,  "H008"},
	{"tenant-009", "São Paulo Business Hotel", "America/Sao_Paulo", "14:00", -23.5489,  -46.6388,  "H009"},
	{"tenant-010", "Toronto Skyline Hotel",    "America/Toronto",   "07:30",  43.6532,  -79.3832,  "H010"},
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
			fmt.Printf("✓ %s | %-35s | %s %s\n", t.TenantID, t.Name, t.RunTime, t.Timezone)
			created++
		} else {
			fmt.Printf("✗ %s | HTTP %d: %s\n", t.TenantID, resp.StatusCode, string(respBody))
			failed++
		}
	}
	fmt.Printf("\nDone. created=%d failed=%d\n", created, failed)
}
