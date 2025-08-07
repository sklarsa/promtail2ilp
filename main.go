package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"
	"github.com/grafana/loki/pkg/push"
)

type PromtailStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]string        `json:"values"`
}

type PromtailPushRequest struct {
	Streams []PromtailStream `json:"streams"`
}

func main() {
	http.HandleFunc("/", handlePromtailPush)

	log.Println("Starting server on :9999")
	log.Println("Waiting for promtail messages...")

	if err := http.ListenAndServe(":9999", nil); err != nil {
		log.Fatal(err)
	}
}

func handlePromtailPush(w http.ResponseWriter, r *http.Request) {
	log.Printf("\n=== New Request ===")
	log.Printf("Method: %s", r.Method)
	log.Printf("URL: %s", r.URL)
	log.Printf("Headers: %v", r.Header)

	var reader io.Reader = r.Body
	defer r.Body.Close()

	contentEncoding := r.Header.Get("Content-Encoding")
	switch contentEncoding {
	case "gzip":
		log.Println("Content is gzipped, decompressing...")
		gzReader, err := gzip.NewReader(r.Body)
		if err != nil {
			log.Printf("Error creating gzip reader: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer gzReader.Close()
		reader = gzReader
	case "snappy":
		log.Println("Content is snappy compressed, decompressing...")
		reader = snappy.NewReader(r.Body)
	default:
		if contentEncoding != "" {
			log.Printf("Unknown content encoding: %s", contentEncoding)
		}
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		log.Printf("Error reading body: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check content type
	contentType := r.Header.Get("Content-Type")
	log.Printf("Content-Type: %s", contentType)

	if contentType == "application/x-protobuf" {
		log.Println("Content is protobuf format - parsing...")
		
		// Try to decompress with snappy first in case it's compressed
		decompressed, err := snappy.Decode(nil, body)
		if err == nil && len(decompressed) > 0 {
			log.Println("Successfully decompressed with snappy")
			body = decompressed
		}

		var pushReq push.PushRequest
		if err := proto.Unmarshal(body, &pushReq); err != nil {
			log.Printf("Error parsing protobuf: %v", err)
			log.Printf("First 100 bytes (hex): %x", body[:min(100, len(body))])
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		log.Printf("\n=== Parsed Protobuf Data ===")
		log.Printf("Number of streams: %d", len(pushReq.Streams))

		for i, stream := range pushReq.Streams {
			log.Printf("\nStream %d:", i+1)
			log.Printf("  Labels: %s", stream.Labels)
			log.Printf("  Number of entries: %d", len(stream.Entries))

			for j, entry := range stream.Entries {
				// Loki uses Unix nanoseconds for timestamps
				t := entry.Timestamp
				log.Printf("    Entry %d: [%s] %s", j+1, t.Format(time.RFC3339), entry.Line)
			}
		}
	} else {
		// Try JSON parsing
		log.Printf("Raw body: %s", string(body))

		var pushReq PromtailPushRequest
		if err := json.Unmarshal(body, &pushReq); err != nil {
			log.Printf("Error parsing JSON: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		log.Printf("\n=== Parsed Data ===")
		log.Printf("Number of streams: %d", len(pushReq.Streams))

		for i, stream := range pushReq.Streams {
			log.Printf("\nStream %d:", i+1)
			log.Printf("  Labels:")
			for k, v := range stream.Stream {
				log.Printf("    %s: %s", k, v)
			}
			log.Printf("  Number of log entries: %d", len(stream.Values))
			for j, entry := range stream.Values {
				if len(entry) >= 2 {
					timestamp := entry[0]
					logLine := entry[1]

					var nanos int64
					_, err := fmt.Sscanf(timestamp, "%d", &nanos)
					if err == nil {
						t := time.Unix(0, nanos)
						log.Printf("    Entry %d: [%s] %s", j+1, t.Format(time.RFC3339), logLine)
					} else {
						log.Printf("    Entry %d: [%s] %s", j+1, timestamp, logLine)
					}
				}
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

type Server struct {
	Port int
}
