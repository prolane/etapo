package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"etapo/internal/collector"
	"etapo/internal/config"
	"etapo/internal/models"
	"etapo/internal/storage"
)

// Server provides the REST API and serves the embedded frontend.
type Server struct {
	cfg       *config.Config
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

	writeJSON(w, http.StatusOK, plugs)
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
	byParam := r.URL.Query().Get("by")
	if byParam != "category" {
		byParam = "plug"
	}

	start, err := breakdownStart(r, rangeParam)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	standby, err := s.standbyPeriod(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	breakdown, err := s.storage.GetBreakdown(r.Context(), rangeParam, byParam, start, s.cfg.Billing.CostPerKWh, standby)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, breakdown)
}

// breakdownStart returns the first moment of the requested local day (date=YYYY-MM-DD)
// or month (month=YYYY-MM). Without a parameter it is the current day or month.
func breakdownStart(r *http.Request, rangeParam string) (time.Time, error) {
	now := time.Now()
	start := now

	if rangeParam == "month" {
		if v := r.URL.Query().Get("month"); v != "" {
			t, err := time.ParseInLocation("2006-01", v, time.Local)
			if err != nil {
				return time.Time{}, fmt.Errorf("month must be formatted as YYYY-MM")
			}
			start = t
		}
	} else if v := r.URL.Query().Get("date"); v != "" {
		t, err := time.ParseInLocation("2006-01-02", v, time.Local)
		if err != nil {
			return time.Time{}, fmt.Errorf("date must be formatted as YYYY-MM-DD")
		}
		start = t
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
		writeJSON(w, http.StatusOK, plug)
		return
	}

	subAction := parts[1]
	switch subAction {
	case "history":
		s.handleHistory(w, r, plugID)
	case "power":
		s.handlePower(w, r, plugID)
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request, plugID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rangeParam := r.URL.Query().Get("range")
	window := 1 * time.Hour
	switch rangeParam {
	case "24h":
		window = 24 * time.Hour
	case "7d":
		window = 7 * 24 * time.Hour
	case "30d":
		window = 30 * 24 * time.Hour
	case "1h", "":
		window = 1 * time.Hour
	}

	since := time.Now().Add(-window)
	points, err := s.storage.GetHistory(r.Context(), plugID, since, 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if points == nil {
		points = []models.HistoryPoint{}
	}

	writeJSON(w, http.StatusOK, points)
}

func (s *Server) handlePower(w http.ResponseWriter, r *http.Request, plugID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		On bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: expected {\"on\": true|false}", http.StatusBadRequest)
		return
	}

	if s.collector == nil {
		http.Error(w, "Collector not configured in serve-only mode", http.StatusServiceUnavailable)
		return
	}

	if err := s.collector.SetPower(r.Context(), plugID, req.On); err != nil {
		http.Error(w, fmt.Sprintf("Failed to toggle socket: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"plug_id": plugID,
		"on":      req.On,
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

	summary, err := s.storage.GetSummary(r.Context(), s.cfg.Billing.Currency, s.cfg.Billing.CostPerKWh, standby)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	summary.StandbyWindowStart = s.cfg.Standby.Start
	summary.StandbyWindowEnd = s.cfg.Standby.End

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
