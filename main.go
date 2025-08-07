package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// Create server configuration
	config := VerboseServerConfig()
	config.Port = 9999
	
	server := NewPromtailServerWithConfig(config)

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
