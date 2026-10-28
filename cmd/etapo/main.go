package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"etapo/internal/api"
	"etapo/internal/collector"
	"etapo/internal/config"
	"etapo/internal/storage"
	"etapo/internal/web"
)

var (
	version = "0.1.0"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "config.yaml", "Path to YAML configuration file")
	flag.Parse()

	// Default fallback to config.example.yaml if config.yaml doesn't exist
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if _, errEx := os.Stat("config.example.yaml"); errEx == nil {
			log.Printf("[Etapo] WARNING: config %q not found, falling back to config.example.yaml (mock devices)", configPath)
			configPath = "config.example.yaml"
		}
	}

	mode := "run"
	args := flag.Args()
	if len(args) > 0 {
		mode = args[0]
	}

	log.Printf("[Etapo v%s] Initializing in %q mode (config: %s)...", version, mode, configPath)

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// 1. Initialize SQLite Storage
	store, err := storage.New(cfg.Storage.DBPath, cfg.FlushDuration(), cfg.Storage.MaxBufferSize)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	defer func() {
		log.Println("[Storage] Flushing pending writes and closing SQLite database...")
		if err := store.Close(); err != nil {
			log.Printf("Storage close error: %v", err)
		}
	}()

	// 2. Setup Context for Graceful Shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// 3. Initialize Broadcaster (SSE)
	sseBroker := api.NewSSEBroker()

	var col *collector.Collector
	if mode == "run" || mode == "collect" {
		col, err = collector.New(cfg, store, sseBroker)
		if err != nil {
			log.Fatalf("Failed to initialize collector: %v", err)
		}
		go col.Start(ctx)
	}

	var srv *api.Server
	if mode == "run" || mode == "serve" {
		srv = api.NewServer(cfg, store, col, sseBroker, web.GetFS())
		go func() {
			if err := srv.Start(); err != nil {
				log.Fatalf("Server failed: %v", err)
			}
		}()
	}

	fmt.Printf("\n⚡ Etapo is running! Open http://localhost:%d in your browser.\n\n", cfg.Server.Port)

	// Await termination signal
	sig := <-sigChan
	log.Printf("[Etapo] Received signal %v, commencing graceful shutdown...", sig)
	cancel()

	if col != nil {
		col.Stop()
	}

	if srv != nil {
		shutdownCtx, sCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer sCancel()
		_ = srv.Shutdown(shutdownCtx)
	}

	log.Println("[Etapo] Exited cleanly.")
}
