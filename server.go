package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"
	"github.com/grafana/loki/pkg/push"
)

// LogLevel represents the logging verbosity level for the server.
// Higher values include all messages from lower levels.
type LogLevel int

const (
	// LogLevelError logs only error messages
	LogLevelError LogLevel = iota
	// LogLevelInfo logs errors and basic informational messages
	LogLevelInfo
	// LogLevelDebug logs errors, info, and debug details about request processing
	LogLevelDebug
	// LogLevelTrace logs all messages including detailed stream content
	LogLevelTrace
)

// ServerConfig contains configuration options for the Promtail server.
type ServerConfig struct {
	// Port specifies the HTTP port to listen on. Use 0 for a random available port.
	Port int
	// LogLevel controls the verbosity of logging output
	LogLevel LogLevel
	// Logger is the logger instance to use. If nil, no logging will occur.
	Logger *log.Logger
}

// DefaultServerConfig returns a ServerConfig with sensible defaults:
// - Port 9999
// - LogLevelInfo logging
// - Default logger
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		Port:     9999,
		LogLevel: LogLevelInfo,
		Logger:   log.Default(),
	}
}

// QuietServerConfig returns a ServerConfig with minimal logging (errors only).
// Useful for tests or production environments where log volume should be minimal.
func QuietServerConfig() *ServerConfig {
	config := DefaultServerConfig()
	config.LogLevel = LogLevelError
	return config
}

// VerboseServerConfig returns a ServerConfig with maximum logging enabled.
// Useful for debugging and development. Logs detailed stream content.
func VerboseServerConfig() *ServerConfig {
	config := DefaultServerConfig()
	config.LogLevel = LogLevelTrace
	return config
}

// PromtailServer is an HTTP server that receives log streams from Promtail
// and processes them. It supports both JSON and protobuf formats with
// optional gzip and snappy compression.
type PromtailServer struct {
	config   *ServerConfig
	server   *http.Server
	listener net.Listener
	handler  func(stream push.Stream)
}

// NewPromtailServer creates a new PromtailServer with default configuration
// listening on the specified port.
func NewPromtailServer(port int) *PromtailServer {
	config := DefaultServerConfig()
	config.Port = port
	return NewPromtailServerWithConfig(config)
}

// NewPromtailServerWithConfig creates a new PromtailServer with the provided configuration.
func NewPromtailServerWithConfig(config *ServerConfig) *PromtailServer {
	return &PromtailServer{
		config: config,
	}
}

// SetHandler sets a custom handler function to process received log streams.
// The handler will be called for each stream after built-in logging is performed.
// If no handler is set, streams will only be logged according to the configured log level.
func (s *PromtailServer) SetHandler(handler func(stream push.Stream)) {
	s.handler = handler
}

func (s *PromtailServer) logStream(stream push.Stream) {
	if s.config.LogLevel >= LogLevelTrace {
		s.config.Logger.Printf("\n=== Stream ===")
		s.config.Logger.Printf("Labels: %s", stream.Labels)
		s.config.Logger.Printf("Number of entries: %d", len(stream.Entries))

		for i, entry := range stream.Entries {
			s.config.Logger.Printf("  Entry %d: [%s] %s", i+1, entry.Timestamp.Format(time.RFC3339), entry.Line)
		}
	}
}

func (s *PromtailServer) logError(format string, v ...interface{}) {
	if s.config.LogLevel >= LogLevelError && s.config.Logger != nil {
		s.config.Logger.Printf("ERROR: "+format, v...)
	}
}

func (s *PromtailServer) logInfo(format string, v ...interface{}) {
	if s.config.LogLevel >= LogLevelInfo && s.config.Logger != nil {
		s.config.Logger.Printf("INFO: "+format, v...)
	}
}

func (s *PromtailServer) logDebug(format string, v ...interface{}) {
	if s.config.LogLevel >= LogLevelDebug && s.config.Logger != nil {
		s.config.Logger.Printf("DEBUG: "+format, v...)
	}
}

func (s *PromtailServer) logTrace(format string, v ...interface{}) {
	if s.config.LogLevel >= LogLevelTrace && s.config.Logger != nil {
		s.config.Logger.Printf("TRACE: "+format, v...)
	}
}

// Start starts the HTTP server and begins listening for Promtail requests.
// The server runs in a separate goroutine and this method returns immediately.
// Returns an error if the server fails to start.
func (s *PromtailServer) Start() error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", s.config.Port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", s.config.Port, err)
	}
	s.listener = listener

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handlePromtailPush)

	s.server = &http.Server{
		Handler: mux,
	}

	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.logError("Server error: %v", err)
		}
	}()

	s.logInfo("Server started on port %d", s.Port())
	return nil
}

// Stop gracefully shuts down the server using the provided context for timeout.
// It will stop accepting new connections and wait for existing requests to complete.
// Returns an error if the shutdown process fails or times out.
func (s *PromtailServer) Stop(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

// Port returns the actual port the server is listening on.
// This is useful when the server was configured with port 0 (random available port).
// Returns the configured port if the server hasn't started yet.
func (s *PromtailServer) Port() int {
	if s.listener != nil {
		if addr, ok := s.listener.Addr().(*net.TCPAddr); ok {
			return addr.Port
		}
	}
	return s.config.Port
}

func (s *PromtailServer) handlePromtailPush(w http.ResponseWriter, r *http.Request) {
	s.logDebug("Received %s request from %s", r.Method, r.RemoteAddr)
	s.logTrace("Request headers: %v", r.Header)

	body, err := s.readRequestBody(r)
	if err != nil {
		s.logError("Error reading request body: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	contentType := r.Header.Get("Content-Type")
	s.logDebug("Processing %s content, body size: %d bytes", contentType, len(body))

	streams, err := s.parseRequestBody(body, contentType)
	if err != nil {
		s.logError("Error parsing request: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.processStreams(streams)
	w.WriteHeader(http.StatusNoContent)
}

func (s *PromtailServer) readRequestBody(r *http.Request) ([]byte, error) {
	var reader io.Reader = r.Body
	defer r.Body.Close()

	contentEncoding := r.Header.Get("Content-Encoding")
	switch contentEncoding {
	case "gzip":
		s.logDebug("Decompressing gzip content")
		gzReader, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, fmt.Errorf("error creating gzip reader: %w", err)
		}
		defer gzReader.Close()
		reader = gzReader
	case "snappy":
		s.logDebug("Decompressing snappy content")
		reader = snappy.NewReader(r.Body)
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("error reading body: %w", err)
	}

	return body, nil
}

func (s *PromtailServer) parseRequestBody(body []byte, contentType string) ([]push.Stream, error) {
	if contentType == "application/x-protobuf" {
		return s.parseProtobuf(body)
	}
	return s.parseJSON(body)
}

func (s *PromtailServer) parseProtobuf(body []byte) ([]push.Stream, error) {
	// Try to decompress with snappy first in case it's compressed
	decompressed, err := snappy.Decode(nil, body)
	if err == nil && len(decompressed) > 0 {
		s.logDebug("Applied additional snappy decompression")
		body = decompressed
	}

	var pushReq push.PushRequest
	if err := proto.Unmarshal(body, &pushReq); err != nil {
		return nil, fmt.Errorf("error parsing protobuf: %w", err)
	}

	s.logDebug("Parsed protobuf with %d streams", len(pushReq.Streams))
	return pushReq.Streams, nil
}

func (s *PromtailServer) parseJSON(body []byte) ([]push.Stream, error) {
	var pushReq struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(body, &pushReq); err != nil {
		return nil, fmt.Errorf("error parsing JSON: %w", err)
	}

	s.logDebug("Parsed JSON with %d streams", len(pushReq.Streams))

	streams := make([]push.Stream, 0, len(pushReq.Streams))
	for _, jsonStream := range pushReq.Streams {
		stream := push.Stream{
			Labels: formatLabels(jsonStream.Stream),
		}

		for _, entry := range jsonStream.Values {
			if len(entry) >= 2 {
				timestamp := entry[0]
				logLine := entry[1]

				var nanos int64
				if _, err := fmt.Sscanf(timestamp, "%d", &nanos); err == nil {
					t := time.Unix(0, nanos)
					stream.Entries = append(stream.Entries, push.Entry{
						Timestamp: t,
						Line:      logLine,
					})
				}
			}
		}

		streams = append(streams, stream)
	}

	return streams, nil
}

func (s *PromtailServer) processStreams(streams []push.Stream) {
	for i, stream := range streams {
		s.logDebug("Processing stream %d: %s with %d entries", i+1, stream.Labels, len(stream.Entries))
		s.logStream(stream)
		if s.handler != nil {
			s.handler(stream)
		}
	}
}

func formatLabels(labels map[string]string) string {
	result := "{"
	first := true
	for k, v := range labels {
		if !first {
			result += ", "
		}
		result += fmt.Sprintf(`%s="%s"`, k, v)
		first = false
	}
	result += "}"
	return result
}
