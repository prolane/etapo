package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"slices"
	"strings"
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

var modes = []string{"run", "collect", "serve"}

func usage() {
	fmt.Fprintf(flag.CommandLine.Output(), `Etapo %s - energy monitoring for Tapo P110/P115 smart sockets

Usage:
  etapo [--config FILE] [run|collect|serve]

Modes:
  run       Collector, storage, API and web UI in one process (default)
  collect   Collector and storage only; use when the API runs separately
  serve     Storage, API and web UI only; reads data written by collect

Options:
`, version)
	flag.PrintDefaults()
	fmt.Fprintf(flag.CommandLine.Output(), `
Flags must come before the mode, for example: etapo --config config.yaml run
`)
}

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "config.yaml", "Path to YAML configuration file")
	flag.Usage = usage
	flag.Parse()

	mode := "run"
	args := flag.Args()
	if len(args) > 1 {
		fmt.Fprintf(os.Stderr, "etapo: too many arguments: %s (flags must come before the mode)\n\n", strings.Join(args, " "))
		flag.Usage()
		os.Exit(2)
	}
	if len(args) == 1 {
		mode = args[0]
	}
	if !slices.Contains(modes, mode) {
		fmt.Fprintf(os.Stderr, "etapo: unknown mode %q (use run, collect or serve)\n\n", mode)
		flag.Usage()
		os.Exit(2)
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		log.Fatalf("Config file %q does not exist. Create it (for example: cp config.example.yaml config.yaml) or pass --config FILE.", configPath)
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

	// Stop in dependency order: API first (no new reads), then the collector
	// (lets in-flight polls finish), then flush and close storage. The context
	// is only cancelled after the collector has stopped, so polls are not aborted.
	if srv != nil {
		shutdownCtx, sCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("[API] Shutdown error: %v", err)
		}
		sCancel()
	}

	if col != nil {
		col.Stop()
	}
	cancel()

	log.Println("[Storage] Flushing pending writes and closing SQLite database...")
	if err := store.Close(); err != nil {
		log.Printf("[Storage] Close error: %v", err)
	}

	log.Println("[Etapo] Exited cleanly.")
}
