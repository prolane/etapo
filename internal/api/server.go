package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
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

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
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

	plugs, err := s.storage.GetPlugs(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, plugs)
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
		plug, err := s.storage.GetPlug(r.Context(), plugID)
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

	summary, err := s.storage.GetSummary(r.Context(), s.cfg.Billing.Currency, s.cfg.Billing.CostPerKWh)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, summary)
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
