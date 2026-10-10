package main

import (
	"context"
	"encoding/json"
	"github.com/Self-Command/paca-plugin-task-checkin/internal/buildinfo"
	"github.com/Self-Command/paca-plugin-task-checkin/internal/worker"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"plugin": worker.PluginID, "version": buildinfo.Version, "source_sha": buildinfo.SourceSHA})
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	w, err := worker.New(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer w.DB.Close()
	address := os.Getenv("LISTEN_ADDR")
	if address == "" {
		address = ":8090"
	}
	server := &http.Server{Addr: address, Handler: w.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 90 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Print("check-in HTTP listener stopped")
			stop()
		}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	cleanup := time.NewTicker(time.Hour)
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = server.Shutdown(shutdown)
			cancel()
			return
		case <-ticker.C:
			tick, cancel := context.WithTimeout(ctx, 45*time.Second)
			if err = w.Tick(tick); err != nil {
				log.Print("check-in worker paused; review host control and settings")
			}
			if err = w.DeliveryTick(tick); err != nil {
				log.Print("Obsidian delivery processing pending")
			}
			cancel()
		case <-cleanup.C:
			clean, cancel := context.WithTimeout(ctx, 45*time.Second)
			if err = w.Cleanup(clean); err != nil {
				log.Print("photo cleanup pending")
			}
			cancel()
		}
	}
}
