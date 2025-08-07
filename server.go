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

type ServerConfig struct {
	Port    int
	Debug   bool
	Verbose bool
	Logger  *log.Logger
}

func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		Port:    9999,
		Debug:   false,
		Verbose: false,
		Logger:  log.Default(),
	}
}

type PromtailServer struct {
	config   *ServerConfig
	server   *http.Server
	listener net.Listener
	handler  func(stream push.Stream)
}

func NewPromtailServer(port int) *PromtailServer {
	config := DefaultServerConfig()
	config.Port = port
	return NewPromtailServerWithConfig(config)
}

func NewPromtailServerWithConfig(config *ServerConfig) *PromtailServer {
	return &PromtailServer{
		config: config,
	}
}

func (s *PromtailServer) SetHandler(handler func(stream push.Stream)) {
	s.handler = handler
}

func (s *PromtailServer) logError(format string, v ...interface{}) {
	if s.config.Logger != nil {
		s.config.Logger.Printf("ERROR: "+format, v...)
	}
}

func (s *PromtailServer) logDebug(format string, v ...interface{}) {
	if s.config.Debug && s.config.Logger != nil {
		s.config.Logger.Printf("DEBUG: "+format, v...)
	}
}

func (s *PromtailServer) logVerbose(format string, v ...interface{}) {
	if s.config.Verbose && s.config.Logger != nil {
		s.config.Logger.Printf("VERBOSE: "+format, v...)
	}
}

func (s *PromtailServer) logInfo(format string, v ...interface{}) {
	if s.config.Logger != nil {
		s.config.Logger.Printf("INFO: "+format, v...)
	}
}

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

func (s *PromtailServer) Stop(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

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
	s.logVerbose("Request headers: %v", r.Header)

	var reader io.Reader = r.Body
	defer r.Body.Close()

	contentEncoding := r.Header.Get("Content-Encoding")
	switch contentEncoding {
	case "gzip":
		s.logDebug("Decompressing gzip content")
		gzReader, err := gzip.NewReader(r.Body)
		if err != nil {
			s.logError("Error creating gzip reader: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer gzReader.Close()
		reader = gzReader
	case "snappy":
		s.logDebug("Decompressing snappy content")
		reader = snappy.NewReader(r.Body)
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		s.logError("Error reading body: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	contentType := r.Header.Get("Content-Type")
	s.logDebug("Processing %s content, body size: %d bytes", contentType, len(body))
	
	if contentType == "application/x-protobuf" {
		// Try to decompress with snappy first in case it's compressed
		decompressed, err := snappy.Decode(nil, body)
		if err == nil && len(decompressed) > 0 {
			s.logDebug("Applied additional snappy decompression")
			body = decompressed
		}

		var pushReq push.PushRequest
		if err := proto.Unmarshal(body, &pushReq); err != nil {
			s.logError("Error parsing protobuf: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.logDebug("Parsed protobuf with %d streams", len(pushReq.Streams))

		// Process streams
		for i, stream := range pushReq.Streams {
			s.logVerbose("Processing stream %d: %s with %d entries", i+1, stream.Labels, len(stream.Entries))
			if s.handler != nil {
				s.handler(stream)
			}
		}
	} else {
		// Handle JSON format
		var pushReq struct {
			Streams []struct {
				Stream map[string]string `json:"stream"`
				Values [][]string        `json:"values"`
			} `json:"streams"`
		}
		
		if err := json.Unmarshal(body, &pushReq); err != nil {
			s.logError("Error parsing JSON: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.logDebug("Parsed JSON with %d streams", len(pushReq.Streams))

		// Convert JSON to push.Stream format
		for i, jsonStream := range pushReq.Streams {
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
			
			s.logVerbose("Processing JSON stream %d: %s with %d entries", i+1, stream.Labels, len(stream.Entries))
			if s.handler != nil {
				s.handler(stream)
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
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