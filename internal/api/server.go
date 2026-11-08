package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"etapo/internal/collector"
	"etapo/internal/config"
	"etapo/internal/models"
	"etapo/internal/storage"
)

// Server provides the REST API and serves the embedded frontend.
type Server struct {
	cfg       *config.Config
	cfgMu     sync.RWMutex // guards the plug addresses in cfg, which change when config.yaml is updated
	storage   *storage.Storage
	collector *collector.Collector
	broker    *SSEBroker
	webFS     fs.FS
	srv       *http.Server
}

// NewServer initializes the HTTP API server.
func NewServer(cfg *config.Config, store *storage.Storage, col *collector.Collector, broker *SSEBroker, webFS fs.FS) *Server {
	s := &Server{
		cfg:       cfg,
		storage:   store,
		collector: col,
		broker:    broker,
		webFS:     webFS,
	}

	mux := http.NewServeMux()

	// API Routes
	mux.HandleFunc("/api/plugs", s.handlePlugs)
	mux.HandleFunc("/api/plugs/", s.handlePlugSubroutes)
	mux.HandleFunc("/api/summary", s.handleSummary)
	mux.HandleFunc("/api/breakdown", s.handleBreakdown)
	mux.HandleFunc("/api/leaderboard/day", s.handleDayLeaderboard)
	mux.HandleFunc("/api/leaderboard/month", s.handleMonthLeaderboard)
	mux.Handle("/api/events", broker)
	mux.HandleFunc("/health", s.handleHealth)

	// Frontend Static Assets
	if webFS != nil {
		mux.Handle("/", http.FileServer(http.FS(webFS)))
	}

	s.srv = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      corsMiddleware(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return s
}

// Start runs the HTTP server.
func (s *Server) Start() error {
	log.Printf("[API] Web Dashboard & API listening on http://%s", s.srv.Addr)
	if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown gracefully stops the HTTP server, ending open SSE streams first.
func (s *Server) Shutdown(ctx context.Context) error {
	s.broker.Close()
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "app": "etapo"})
}

func (s *Server) handlePlugs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	standby, err := s.standbyPeriod(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	plugs, err := s.storage.GetPlugs(r.Context(), s.cfg.Billing.CostPerKWh, standby)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.annotatePlugs(plugs)
	writeJSON(w, http.StatusOK, plugs)
}

// annotatePlugs adds what storage does not know: whether each plug is online, and its address in config.yaml.
func (s *Server) annotatePlugs(plugs []models.PlugWithLatest) {
	s.cfgMu.RLock()
	configIPs := make(map[string]string)
	for _, pc := range s.cfg.Plugs {
		if pc.Type == "tapo" {
			configIPs[pc.ID] = pc.IP
		}
	}
	s.cfgMu.RUnlock()

	now := time.Now()
	for i := range plugs {
		plugs[i].ConfigIP = configIPs[plugs[i].ID]
		plugs[i].Online = models.Online(plugs[i].LatestReading, now, s.cfg.OfflineAfter())
	}
}

func (s *Server) handleBreakdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rangeParam := r.URL.Query().Get("range")
	if rangeParam != "month" {
		rangeParam = "today"
	}
	if by := r.URL.Query().Get("by"); by != "" && by != "plug" {
		http.Error(w, "by must be plug", http.StatusBadRequest)
		return
	}

	start, err := periodStart(r, rangeParam)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	breakdown, err := s.storage.GetBreakdown(r.Context(), rangeParam, start)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, breakdown)
}

func (s *Server) handleDayLeaderboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	start, err := periodStart(r, "today")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	stats, err := s.storage.GetDayStats(r.Context(), start, s.cfg.Billing.CostPerKWh)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if stats == nil {
		stats = []models.PeriodStats{}
	}

	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleMonthLeaderboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	start, err := periodStart(r, "month")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	stats, err := s.storage.GetMonthStats(r.Context(), start, s.cfg.Billing.CostPerKWh)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if stats == nil {
		stats = []models.PeriodStats{}
	}

	writeJSON(w, http.StatusOK, stats)
}

// periodStart returns the first moment of the requested local day (date=YYYY-MM-DD)
// or month (month=YYYY-MM). Without a parameter it is the current day or month.
func periodStart(r *http.Request, rangeParam string) (time.Time, error) {
	now := time.Now()
	start := now

	if rangeParam == "month" {
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
		if v := r.URL.Query().Get("month"); v != "" {
			t, err := time.ParseInLocation("2006-01", v, time.Local)
			if err != nil {
				return time.Time{}, fmt.Errorf("month must be formatted as YYYY-MM")
			}
			start = t
		}
	} else {
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
		if v := r.URL.Query().Get("date"); v != "" {
			t, err := time.ParseInLocation("2006-01-02", v, time.Local)
			if err != nil {
				return time.Time{}, fmt.Errorf("date must be formatted as YYYY-MM-DD")
			}
			start = t
		}
	}

	if start.After(now) {
		return time.Time{}, fmt.Errorf("cannot show a day or month that has not started yet")
	}
	return start, nil
}

func (s *Server) handlePlugSubroutes(w http.ResponseWriter, r *http.Request) {
	// Paths: /api/plugs/{id} or /api/plugs/{id}/history or /api/plugs/{id}/power
	path := strings.TrimPrefix(r.URL.Path, "/api/plugs/")
	parts := strings.Split(path, "/")
	plugID := parts[0]

	if len(parts) == 1 {
		// GET /api/plugs/{id}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		standby, err := s.standbyPeriod(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		plug, err := s.storage.GetPlug(r.Context(), plugID, s.cfg.Billing.CostPerKWh, standby)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if plug == nil {
			http.Error(w, "Plug not found", http.StatusNotFound)
			return
		}
		plugs := []models.PlugWithLatest{*plug}
		s.annotatePlugs(plugs)
		writeJSON(w, http.StatusOK, plugs[0])
		return
	}

	subAction := parts[1]
	switch subAction {
	case "history":
		s.handleHistory(w, r, plugID)
	case "power":
		s.handlePower(w, r, plugID)
	case "config-ip":
		s.handleConfigIP(w, r, plugID)
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

// handleConfigIP writes the address a plug was found at into config.yaml. The client names the address it expects,
// so a plug that has moved again since the dashboard was loaded is not written with an address nobody confirmed.
func (s *Server) handleConfigIP(w http.ResponseWriter, r *http.Request, plugID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IP == "" {
		http.Error(w, `Invalid request body: expected {"ip": "x.x.x.x"}`, http.StatusBadRequest)
		return
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	configured, isTapo := s.configuredTapoIP(plugID)
	if !isTapo {
		http.Error(w, "Plug is not a Tapo plug in the configuration", http.StatusNotFound)
		return
	}
	current, exists, err := s.storage.PlugIP(r.Context(), plugID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "Plug not found", http.StatusNotFound)
		return
	}
	if current != req.IP {
		http.Error(w, fmt.Sprintf("The plug is now at %s, not %s. Reload the dashboard.", current, req.IP), http.StatusConflict)
		return
	}
	if configured == current {
		http.Error(w, "config.yaml already uses this address", http.StatusConflict)
		return
	}
	if s.cfg.Path() == "" {
		http.Error(w, "etapo was started without a configuration file, so config.yaml cannot be updated", http.StatusConflict)
		return
	}
	if err := config.SetPlugIP(s.cfg.Path(), plugID, current); err != nil {
		http.Error(w, fmt.Sprintf("Failed to update config.yaml: %v", err), http.StatusInternalServerError)
		return
	}
	s.setConfiguredIP(plugID, current)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"plug_id": plugID,
		"ip":      current,
	})
}

// configuredTapoIP returns the address of a Tapo plug in the configuration. The caller must hold cfgMu.
func (s *Server) configuredTapoIP(plugID string) (string, bool) {
	for _, pc := range s.cfg.Plugs {
		if pc.ID == plugID && pc.Type == "tapo" {
			return pc.IP, true
		}
	}
	return "", false
}

// setConfiguredIP updates the in-memory address of a plug after config.yaml was written. The caller must hold cfgMu.
func (s *Server) setConfiguredIP(plugID, ip string) {
	for i := range s.cfg.Plugs {
		if s.cfg.Plugs[i].ID == plugID {
			s.cfg.Plugs[i].IP = ip
		}
	}
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request, plugID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	start, end, bucket, err := historyWindow(r, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	points, err := s.storage.GetHistory(r.Context(), plugID, start, end, bucket)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if points == nil {
		points = []models.HistoryPoint{}
	}

	writeJSON(w, http.StatusOK, points)
}

// historyWindow returns the period and bucket size for a history request. The rolling
// ranges are 1h (5 minute buckets), 24h (5 minutes), 7d (1 hour) and 30d (4 hours);
// range=day is a local calendar day (date=YYYY-MM-DD, default today) in 5 minute buckets,
// and range=month is a calendar month (month=YYYY-MM, local time) in 4 hour buckets.
// A missing range means 24h. An unknown range is an error.
func historyWindow(r *http.Request, now time.Time) (start, end time.Time, bucket time.Duration, err error) {
	end = now
	switch r.URL.Query().Get("range") {
	case "day", "today":
		bucket = 5 * time.Minute
		start, err = periodStart(r, "today")
		if err != nil {
			return time.Time{}, time.Time{}, 0, err
		}
		end = start.AddDate(0, 0, 1)
		if end.After(now) {
			end = now
		}
	case "1h":
		bucket = 5 * time.Minute
		start = now.Add(-time.Hour).Truncate(bucket)
	case "", "24h":
		bucket = 5 * time.Minute
		start = now.Add(-24 * time.Hour).Truncate(bucket)
	case "7d":
		bucket = time.Hour
		start = now.Add(-7 * 24 * time.Hour).Truncate(bucket)
	case "30d":
		bucket = 4 * time.Hour
		start = now.Add(-30 * 24 * time.Hour).Truncate(bucket)
	case "month":
		bucket = 4 * time.Hour
		start, err = periodStart(r, "month")
		if err != nil {
			return time.Time{}, time.Time{}, 0, err
		}
		end = start.AddDate(0, 1, 0)
		if end.After(now) {
			end = now
		}
	default:
		return time.Time{}, time.Time{}, 0, fmt.Errorf("range must be 1h, 24h, day, 7d, 30d or month")
	}
	return start, end, bucket, nil
}

func (s *Server) handlePower(w http.ResponseWriter, r *http.Request, plugID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		On *bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.On == nil {
		http.Error(w, "Invalid request body: expected {\"on\": true|false}", http.StatusBadRequest)
		return
	}

	if s.collector == nil {
		http.Error(w, "Collector not configured in serve-only mode", http.StatusServiceUnavailable)
		return
	}

	if err := s.collector.SetPower(r.Context(), plugID, *req.On); err != nil {
		if errors.Is(err, collector.ErrPlugNotFound) {
			http.Error(w, "Plug not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("Failed to toggle socket: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"plug_id": plugID,
		"on":      *req.On,
	})
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	standby, err := s.standbyPeriod(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	summary, err := s.storage.GetSummary(r.Context(), s.cfg.Billing.Currency, s.cfg.Billing.CostPerKWh, standby, s.cfg.OfflineAfter())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	summary.StandbyWindowStart = s.cfg.Standby.Start
	summary.StandbyWindowEnd = s.cfg.Standby.End
	summary.OfflineAfterSec = int(s.cfg.OfflineAfter().Seconds())

	writeJSON(w, http.StatusOK, summary)
}

// standbyPeriod returns the configured standby window and the number of nights from ?nights=N (default 7).
func (s *Server) standbyPeriod(r *http.Request) (models.StandbyPeriod, error) {
	start, end, err := s.cfg.StandbyMinutes()
	if err != nil {
		return models.StandbyPeriod{}, err
	}
	nights := 7
	if v := r.URL.Query().Get("nights"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			return models.StandbyPeriod{}, fmt.Errorf("nights must be a whole number from 1 to 365")
		}
		nights = n
	}
	return models.StandbyPeriod{StartMinute: start, EndMinute: end, Nights: nights}, nil
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
