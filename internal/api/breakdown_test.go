package api

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestBreakdownStart(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name       string
		query      string
		rangeParam string
		wantErr    bool
	}{
		{name: "default day", query: "", rangeParam: "today"},
		{name: "past day", query: "date=2000-01-02", rangeParam: "today"},
		{name: "future day", query: "date=" + now.AddDate(0, 0, 2).Format("2006-01-02"), rangeParam: "today", wantErr: true},
		{name: "malformed day", query: "date=28-10-2026", rangeParam: "today", wantErr: true},
		{name: "default month", query: "", rangeParam: "month"},
		{name: "past month", query: "month=2000-01", rangeParam: "month"},
		{name: "future month", query: "month=" + now.AddDate(0, 2, 0).Format("2006-01"), rangeParam: "month", wantErr: true},
		{name: "malformed month", query: "month=2026-13", rangeParam: "month", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/breakdown?"+tt.query, nil)
			_, err := breakdownStart(r, tt.rangeParam)
			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got %v", tt.wantErr, err)
			}
		})
	}
}
