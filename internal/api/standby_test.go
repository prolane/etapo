package api

import (
	"net/http/httptest"
	"testing"

	"etapo/internal/config"
)

func TestStandbyPeriodNights(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig()}
	tests := []struct {
		query      string
		wantNights int
		wantErr    bool
	}{
		{query: "", wantNights: 7},
		{query: "nights=1", wantNights: 1},
		{query: "nights=30", wantNights: 30},
		{query: "nights=0", wantErr: true},
		{query: "nights=366", wantErr: true},
		{query: "nights=last", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/summary?"+tt.query, nil)
			period, err := s.standbyPeriod(r)
			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got %v", tt.wantErr, err)
			}
			if err == nil && period.Nights != tt.wantNights {
				t.Fatalf("expected %d nights, got %d", tt.wantNights, period.Nights)
			}
			if err == nil && (period.StartMinute != 120 || period.EndMinute != 300) {
				t.Fatalf("expected default window 02:00-05:00, got %d-%d", period.StartMinute, period.EndMinute)
			}
		})
	}
}
