package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/grafana/loki/pkg/push"
	qdb "github.com/questdb/go-questdb-client/v3"
)

type ILPWriterConfig struct {
	OnSuccess func(stream push.Stream)
	OnError   func(stream push.Stream, err error)
}

type ILPWriter struct {
	sender qdb.LineSender
	config *ILPWriterConfig
}

func NewILPWriter(addr string) (*ILPWriter, error) {
	return NewILPWriterWithConfig(addr, nil)
}

func NewILPWriterWithConfig(addr string, config *ILPWriterConfig) (*ILPWriter, error) {
	sender, err := qdb.LineSenderFromConf(context.Background(), addr)
	if err != nil {
		return nil, fmt.Errorf("failed to create ILP sender: %w", err)
	}

	return &ILPWriter{
		sender: sender,
		config: config,
	}, nil
}

func (w *ILPWriter) WriteStream(stream push.Stream) error {
	// Parse labels from the stream
	labels := parseLabels(stream.Labels)

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
		if w.config != nil && w.config.OnError != nil {
			w.config.OnError(stream, err)
		}
		return fmt.Errorf("failed to flush ILP data: %w", err)
	}

	if w.config != nil && w.config.OnSuccess != nil {
		w.config.OnSuccess(stream)
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

// StreamHandler returns a handler function that writes streams to QuestDB
func (w *ILPWriter) StreamHandler() func(stream push.Stream) {
	return func(stream push.Stream) {
		w.WriteStream(stream)
	}
}
