package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/grafana/loki/pkg/push"
)

func main() {
	// Create server configuration
	config := DefaultServerConfig()
	config.Port = 9999
	config.Debug = true   // Enable debug logging
	config.Verbose = true // Enable verbose logging
	
	server := NewPromtailServerWithConfig(config)
	
	// Set up a handler that logs the streams when verbose mode is on
	server.SetHandler(func(stream push.Stream) {
		if config.Verbose {
			config.Logger.Printf("\n=== Stream ===")
			config.Logger.Printf("Labels: %s", stream.Labels)
			config.Logger.Printf("Number of entries: %d", len(stream.Entries))
			
			for i, entry := range stream.Entries {
				config.Logger.Printf("  Entry %d: [%s] %s", i+1, entry.Timestamp.Format(time.RFC3339), entry.Line)
			}
		}
	})

	if err := server.Start(); err != nil {
		log.Fatal(err)
	}

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	config.Logger.Println("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	if err := server.Stop(ctx); err != nil {
		config.Logger.Printf("Error shutting down server: %v", err)
	}
}
