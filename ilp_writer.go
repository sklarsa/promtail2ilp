package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/grafana/loki/pkg/push"
	qdb "github.com/questdb/go-questdb-client/v3"
)

type ILPWriter struct {
	sender qdb.LineSender
}

func NewILPWriter(addr string) (*ILPWriter, error) {
	sender, err := qdb.LineSenderFromConf(context.Background(), addr)
	if err != nil {
		return nil, fmt.Errorf("failed to create ILP sender: %w", err)
	}
	
	return &ILPWriter{
		sender: sender,
	}, nil
}

func (w *ILPWriter) WriteStream(stream push.Stream) error {
	// Parse labels from the stream
	labels := parseLabels(stream.Labels)
	log.Printf("Parsed labels: %v", labels)
	
	for _, entry := range stream.Entries {
		// Start building the ILP line
		w.sender.Table("logs")
		
		// Add log line as a field
		w.sender.StringColumn("log", entry.Line)
		
		// Add labels as tags
		for k, v := range labels {
			// QuestDB requires tag values to be symbols (not strings)
			w.sender.Symbol(k, v)
		}
		
		// Set timestamp
		w.sender.At(context.Background(), entry.Timestamp)
	}
	
	// Flush the data
	err := w.sender.Flush(context.Background())
	if err != nil {
		return fmt.Errorf("failed to flush ILP data: %w", err)
	}
	
	return nil
}

func (w *ILPWriter) Close() error {
	if w.sender != nil {
		return w.sender.Close(context.Background())
	}
	return nil
}

// parseLabels parses the label string format: {key1="value1", key2="value2"}
func parseLabels(labelStr string) map[string]string {
	labels := make(map[string]string)
	
	// Remove curly braces
	labelStr = strings.Trim(labelStr, "{}")
	
	// Split by comma
	pairs := strings.Split(labelStr, ",")
	
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.Trim(strings.TrimSpace(parts[1]), `"`)
			labels[key] = value
		}
	}
	
	return labels
}

// CreateLogsTableSQL returns the SQL to create the logs table in QuestDB
func CreateLogsTableSQL() string {
	return `
CREATE TABLE IF NOT EXISTS logs (
	log STRING,
	filename SYMBOL,
	ip SYMBOL,
	job SYMBOL,
	method SYMBOL,
	path SYMBOL,
	status SYMBOL,
	timestamp TIMESTAMP
) timestamp(timestamp) PARTITION BY DAY;
`
}

// StreamHandler returns a handler function that writes streams to QuestDB
func (w *ILPWriter) StreamHandler() func(stream push.Stream) {
	return func(stream push.Stream) {
		if err := w.WriteStream(stream); err != nil {
			log.Printf("Error writing stream to QuestDB: %v", err)
		} else {
			log.Printf("Successfully wrote stream to QuestDB: %s", stream.Labels)
		}
	}
}