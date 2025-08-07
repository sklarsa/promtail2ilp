package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sklarsa/promtail2ilp/server"
)

func main() {
	// Define command-line flags
	var (
		port     = flag.Int("port", 9999, "HTTP port to listen on")
		logLevel = flag.String("log-level", "info", "Log level: error, info, debug, trace")
		help     = flag.Bool("help", false, "Show help message")
	)

	flag.Usage = func() {
		log.Printf("Usage: %s [options]\n", os.Args[0])
		log.Println("\nPromtail to QuestDB bridge server")
		log.Println("Receives log streams from Promtail and forwards them to QuestDB via ILP")
		log.Println("\nOptions:")
		flag.PrintDefaults()
		log.Println("\nLog levels:")
		log.Println("  error  - Only error messages")
		log.Println("  info   - Errors and basic info (default)")
		log.Println("  debug  - Errors, info, and debug details")
		log.Println("  trace  - All messages including detailed stream content")
	}

	flag.Parse()

	if *help {
		flag.Usage()
		return
	}

	// Parse log level
	var logLevelEnum server.LogLevel
	switch *logLevel {
	case "error":
		logLevelEnum = server.LogLevelError
	case "info":
		logLevelEnum = server.LogLevelInfo
	case "debug":
		logLevelEnum = server.LogLevelDebug
	case "trace":
		logLevelEnum = server.LogLevelTrace
	default:
		log.Fatalf("Invalid log level: %s. Valid options: error, info, debug, trace", *logLevel)
	}

	// Create server configuration
	config := server.DefaultServerConfig()
	config.Port = *port
	config.LogLevel = logLevelEnum

	promtailServer := server.NewPromtailServerWithConfig(config)

	if err := promtailServer.Start(); err != nil {
		log.Fatal(err)
	}

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	config.Logger.Println("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := promtailServer.Stop(ctx); err != nil {
		config.Logger.Printf("Error shutting down server: %v", err)
	}
}
