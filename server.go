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

type PromtailServer struct {
	port     int
	server   *http.Server
	listener net.Listener
	handler  func(stream push.Stream)
}

func NewPromtailServer(port int) *PromtailServer {
	return &PromtailServer{
		port: port,
	}
}

func (s *PromtailServer) SetHandler(handler func(stream push.Stream)) {
	s.handler = handler
}

func (s *PromtailServer) Start() error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", s.port, err)
	}
	s.listener = listener

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handlePromtailPush)

	s.server = &http.Server{
		Handler: mux,
	}

	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("Server error: %v", err)
		}
	}()

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
	return s.port
}

func (s *PromtailServer) handlePromtailPush(w http.ResponseWriter, r *http.Request) {
	var reader io.Reader = r.Body
	defer r.Body.Close()

	contentEncoding := r.Header.Get("Content-Encoding")
	switch contentEncoding {
	case "gzip":
		gzReader, err := gzip.NewReader(r.Body)
		if err != nil {
			log.Printf("Error creating gzip reader: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer gzReader.Close()
		reader = gzReader
	case "snappy":
		reader = snappy.NewReader(r.Body)
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		log.Printf("Error reading body: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	contentType := r.Header.Get("Content-Type")
	
	if contentType == "application/x-protobuf" {
		// Try to decompress with snappy first in case it's compressed
		decompressed, err := snappy.Decode(nil, body)
		if err == nil && len(decompressed) > 0 {
			body = decompressed
		}

		var pushReq push.PushRequest
		if err := proto.Unmarshal(body, &pushReq); err != nil {
			log.Printf("Error parsing protobuf: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Process streams
		for _, stream := range pushReq.Streams {
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
			log.Printf("Error parsing JSON: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Convert JSON to push.Stream format
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