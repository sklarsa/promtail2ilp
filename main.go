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
	server := NewPromtailServer(9999)
	
	// Set up a handler that logs the streams
	server.SetHandler(func(stream push.Stream) {
		log.Printf("\n=== Stream ===")
		log.Printf("Labels: %s", stream.Labels)
		log.Printf("Number of entries: %d", len(stream.Entries))
		
		for i, entry := range stream.Entries {
			log.Printf("  Entry %d: [%s] %s", i+1, entry.Timestamp.Format(time.RFC3339), entry.Line)
		}
	})

	log.Printf("Starting server on port %d", server.port)
	if err := server.Start(); err != nil {
		log.Fatal(err)
	}
	
	log.Printf("Server listening on port %d", server.Port())

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	if err := server.Stop(ctx); err != nil {
		log.Printf("Error shutting down server: %v", err)
	}
}
