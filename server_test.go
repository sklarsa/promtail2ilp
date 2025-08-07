package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"
	"github.com/grafana/loki/pkg/push"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
)

func TestBasicE2E(t *testing.T) {
	ctx := context.Background()

	// Start our server with minimal logging for tests
	config := QuietServerConfig()
	config.Port = 0 // Use port 0 to get a random available port

	server := NewPromtailServerWithConfig(config)

	// Collect received streams
	var receivedStreams []push.Stream
	server.SetHandler(func(stream push.Stream) {
		receivedStreams = append(receivedStreams, stream)
		t.Logf("Received stream with labels: %s, entries: %d", stream.Labels, len(stream.Entries))
	})

	require.NoError(t, server.Start())
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Stop(shutdownCtx)
	})

	targetURL := fmt.Sprintf("http://localhost:%d", server.Port())
	targetLogDir := "/logs"

	promtailConf := fmt.Sprintf(`
server:
  http_listen_port: 9080
  grpc_listen_port: 0

positions:
  filename: /tmp/positions.yaml

clients:
  - url: %s

scrape_configs:
  - job_name: apache-logs
    static_configs:
      - targets:
          - localhost
        labels:
          job: apache
          __path__: %s/*.log
    pipeline_stages:
      - regex:
          expression: '^(?P<ip>\S+) \S+ \S+ \[(?P<timestamp>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) \S+" (?P<status>\d+) (?P<size>\d+)'
      - labels:
          ip:
          method:
          path:
          status:
      - timestamp:
          source: timestamp
          format: 02/Jan/2006:15:04:05 -0700
`, targetURL, targetLogDir)

	inputLogs, err := os.Open("./apache_logs.txt")
	require.NoError(t, err)
	t.Cleanup(func() {
		inputLogs.Close()
	})

	req := testcontainers.ContainerRequest{
		Image:        "grafana/promtail:2.9.0",
		ExposedPorts: []string{"9080/tcp"},
		Cmd:          []string{"-config.file=/etc/promtail/config.yml"},
		Files: []testcontainers.ContainerFile{
			{
				Reader:            inputLogs,
				ContainerFilePath: filepath.Join(targetLogDir, "apache.log"),
			},
			{
				Reader:            strings.NewReader(promtailConf),
				ContainerFilePath: "/etc/promtail/config.yml",
			},
		},
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.NetworkMode = "host"
		},
	}

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctr.Terminate(ctx)
	})

	// Wait for promtail to send data
	require.Eventually(t, func() bool {
		return len(receivedStreams) > 0
	}, 15*time.Second, 100*time.Millisecond, "Should have received at least one stream")

	t.Logf("Received %d total streams", len(receivedStreams))

	// Log some sample data
	for i, stream := range receivedStreams[:min(5, len(receivedStreams))] {
		t.Logf("Stream %d: %s", i+1, stream.Labels)
		if len(stream.Entries) > 0 {
			t.Logf("  First entry: %s", stream.Entries[0].Line)
		}
	}
}

func TestPromtailServer_readRequestBody(t *testing.T) {
	server := NewPromtailServerWithConfig(QuietServerConfig())

	t.Run("uncompressed body", func(t *testing.T) {
		body := "test body content"
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))

		result, err := server.readRequestBody(req)

		require.NoError(t, err)
		assert.Equal(t, body, string(result))
	})

	t.Run("gzip compressed body", func(t *testing.T) {
		originalBody := "test body content"

		var buf bytes.Buffer
		gzWriter := gzip.NewWriter(&buf)
		_, err := gzWriter.Write([]byte(originalBody))
		require.NoError(t, err)
		require.NoError(t, gzWriter.Close())

		req := httptest.NewRequest("POST", "/", &buf)
		req.Header.Set("Content-Encoding", "gzip")

		result, err := server.readRequestBody(req)

		require.NoError(t, err)
		assert.Equal(t, originalBody, string(result))
	})

	t.Run("snappy compressed body", func(t *testing.T) {
		originalBody := "test body content that is longer to compress well with snappy algorithm"

		// Create a snappy-compressed stream using snappy.NewBufferedWriter
		var buf bytes.Buffer
		writer := snappy.NewBufferedWriter(&buf)
		_, err := writer.Write([]byte(originalBody))
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		req := httptest.NewRequest("POST", "/", &buf)
		req.Header.Set("Content-Encoding", "snappy")

		result, err := server.readRequestBody(req)

		require.NoError(t, err)
		assert.Equal(t, originalBody, string(result))
	})

	t.Run("invalid gzip data", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", strings.NewReader("invalid gzip data"))
		req.Header.Set("Content-Encoding", "gzip")

		_, err := server.readRequestBody(req)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "error creating gzip reader")
	})
}

func TestPromtailServer_parseProtobuf(t *testing.T) {
	server := NewPromtailServerWithConfig(QuietServerConfig())

	t.Run("valid protobuf", func(t *testing.T) {
		// Create a test push request
		timestamp := time.Now()
		pushReq := push.PushRequest{
			Streams: []push.Stream{
				{
					Labels: `{job="test", instance="localhost"}`,
					Entries: []push.Entry{
						{
							Timestamp: timestamp,
							Line:      "test log line 1",
						},
						{
							Timestamp: timestamp.Add(time.Second),
							Line:      "test log line 2",
						},
					},
				},
			},
		}

		data, err := proto.Marshal(&pushReq)
		require.NoError(t, err)

		streams, err := server.parseProtobuf(data)

		require.NoError(t, err)
		require.Len(t, streams, 1)
		assert.Equal(t, `{job="test", instance="localhost"}`, streams[0].Labels)
		require.Len(t, streams[0].Entries, 2)
		assert.Equal(t, "test log line 1", streams[0].Entries[0].Line)
		assert.Equal(t, "test log line 2", streams[0].Entries[1].Line)
	})

	t.Run("snappy compressed protobuf", func(t *testing.T) {
		pushReq := push.PushRequest{
			Streams: []push.Stream{
				{
					Labels: `{job="test"}`,
					Entries: []push.Entry{
						{
							Timestamp: time.Now(),
							Line:      "compressed test log",
						},
					},
				},
			},
		}

		data, err := proto.Marshal(&pushReq)
		require.NoError(t, err)

		compressed := snappy.Encode(nil, data)

		streams, err := server.parseProtobuf(compressed)

		require.NoError(t, err)
		require.Len(t, streams, 1)
		assert.Equal(t, `{job="test"}`, streams[0].Labels)
		assert.Equal(t, "compressed test log", streams[0].Entries[0].Line)
	})

	t.Run("invalid protobuf data", func(t *testing.T) {
		_, err := server.parseProtobuf([]byte("invalid protobuf data"))

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "error parsing protobuf")
	})
}

func TestPromtailServer_parseJSON(t *testing.T) {
	server := NewPromtailServerWithConfig(QuietServerConfig())

	t.Run("valid JSON", func(t *testing.T) {
		jsonData := `{
			"streams": [
				{
					"stream": {
						"job": "test",
						"instance": "localhost"
					},
					"values": [
						["1640995200000000000", "test log line 1"],
						["1640995201000000000", "test log line 2"]
					]
				}
			]
		}`

		streams, err := server.parseJSON([]byte(jsonData))

		require.NoError(t, err)
		require.Len(t, streams, 1)
		assert.Equal(t, `{job="test", instance="localhost"}`, streams[0].Labels)
		require.Len(t, streams[0].Entries, 2)
		assert.Equal(t, "test log line 1", streams[0].Entries[0].Line)
		assert.Equal(t, "test log line 2", streams[0].Entries[1].Line)

		// Check timestamps
		expectedTime1 := time.Unix(0, 1640995200000000000)
		expectedTime2 := time.Unix(0, 1640995201000000000)
		assert.Equal(t, expectedTime1, streams[0].Entries[0].Timestamp)
		assert.Equal(t, expectedTime2, streams[0].Entries[1].Timestamp)
	})

	t.Run("multiple streams", func(t *testing.T) {
		jsonData := `{
			"streams": [
				{
					"stream": {"job": "app1"},
					"values": [["1640995200000000000", "app1 log"]]
				},
				{
					"stream": {"job": "app2"},
					"values": [["1640995201000000000", "app2 log"]]
				}
			]
		}`

		streams, err := server.parseJSON([]byte(jsonData))

		require.NoError(t, err)
		require.Len(t, streams, 2)
		assert.Equal(t, `{job="app1"}`, streams[0].Labels)
		assert.Equal(t, `{job="app2"}`, streams[1].Labels)
		assert.Equal(t, "app1 log", streams[0].Entries[0].Line)
		assert.Equal(t, "app2 log", streams[1].Entries[0].Line)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		_, err := server.parseJSON([]byte("invalid json"))

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "error parsing JSON")
	})

	t.Run("invalid timestamp", func(t *testing.T) {
		jsonData := `{
			"streams": [
				{
					"stream": {"job": "test"},
					"values": [["invalid_timestamp", "test log"]]
				}
			]
		}`

		streams, err := server.parseJSON([]byte(jsonData))

		require.NoError(t, err)
		require.Len(t, streams, 1)
		// Should skip entries with invalid timestamps
		assert.Len(t, streams[0].Entries, 0)
	})

	t.Run("incomplete values", func(t *testing.T) {
		jsonData := `{
			"streams": [
				{
					"stream": {"job": "test"},
					"values": [
						["1640995200000000000"],
						["1640995201000000000", "valid log"]
					]
				}
			]
		}`

		streams, err := server.parseJSON([]byte(jsonData))

		require.NoError(t, err)
		require.Len(t, streams, 1)
		// Should only include complete entries
		assert.Len(t, streams[0].Entries, 1)
		assert.Equal(t, "valid log", streams[0].Entries[0].Line)
	})
}

func TestPromtailServer_processStreams(t *testing.T) {
	var processedStreams []push.Stream

	config := QuietServerConfig()
	server := NewPromtailServerWithConfig(config)
	server.SetHandler(func(stream push.Stream) {
		processedStreams = append(processedStreams, stream)
	})

	streams := []push.Stream{
		{
			Labels: `{job="test1"}`,
			Entries: []push.Entry{
				{Timestamp: time.Now(), Line: "log line 1"},
			},
		},
		{
			Labels: `{job="test2"}`,
			Entries: []push.Entry{
				{Timestamp: time.Now(), Line: "log line 2"},
			},
		},
	}

	server.processStreams(streams)

	require.Len(t, processedStreams, 2)
	assert.Equal(t, `{job="test1"}`, processedStreams[0].Labels)
	assert.Equal(t, `{job="test2"}`, processedStreams[1].Labels)
	assert.Equal(t, "log line 1", processedStreams[0].Entries[0].Line)
	assert.Equal(t, "log line 2", processedStreams[1].Entries[0].Line)
}

func TestPromtailServer_handlePromtailPush_Integration(t *testing.T) {
	var receivedStreams []push.Stream

	server := NewPromtailServerWithConfig(QuietServerConfig())
	server.SetHandler(func(stream push.Stream) {
		receivedStreams = append(receivedStreams, stream)
	})

	t.Run("JSON request", func(t *testing.T) {
		receivedStreams = nil // Reset

		jsonData := `{
			"streams": [
				{
					"stream": {"job": "test", "level": "info"},
					"values": [["1640995200000000000", "test log message"]]
				}
			]
		}`

		req := httptest.NewRequest("POST", "/", strings.NewReader(jsonData))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handlePromtailPush(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)
		require.Len(t, receivedStreams, 1)
		// Map iteration order is not deterministic, so check content rather than exact format
		labels := receivedStreams[0].Labels
		assert.Contains(t, labels, `job="test"`)
		assert.Contains(t, labels, `level="info"`)
		assert.True(t, strings.HasPrefix(labels, "{"))
		assert.True(t, strings.HasSuffix(labels, "}"))
		assert.Equal(t, "test log message", receivedStreams[0].Entries[0].Line)
	})

	t.Run("protobuf request", func(t *testing.T) {
		receivedStreams = nil // Reset

		pushReq := push.PushRequest{
			Streams: []push.Stream{
				{
					Labels: `{job="protobuf-test"}`,
					Entries: []push.Entry{
						{
							Timestamp: time.Unix(0, 1640995200000000000),
							Line:      "protobuf log message",
						},
					},
				},
			},
		}

		data, err := proto.Marshal(&pushReq)
		require.NoError(t, err)

		req := httptest.NewRequest("POST", "/", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/x-protobuf")
		w := httptest.NewRecorder()

		server.handlePromtailPush(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)
		require.Len(t, receivedStreams, 1)
		assert.Equal(t, `{job="protobuf-test"}`, receivedStreams[0].Labels)
		assert.Equal(t, "protobuf log message", receivedStreams[0].Entries[0].Line)
	})

	t.Run("gzip compressed JSON request", func(t *testing.T) {
		receivedStreams = nil // Reset

		jsonData := `{
			"streams": [
				{
					"stream": {"job": "gzip-test"},
					"values": [["1640995200000000000", "gzipped log message"]]
				}
			]
		}`

		var buf bytes.Buffer
		gzWriter := gzip.NewWriter(&buf)
		_, err := gzWriter.Write([]byte(jsonData))
		require.NoError(t, err)
		require.NoError(t, gzWriter.Close())

		req := httptest.NewRequest("POST", "/", &buf)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Content-Encoding", "gzip")
		w := httptest.NewRecorder()

		server.handlePromtailPush(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)
		require.Len(t, receivedStreams, 1)
		assert.Equal(t, `{job="gzip-test"}`, receivedStreams[0].Labels)
		assert.Equal(t, "gzipped log message", receivedStreams[0].Entries[0].Line)
	})

	t.Run("invalid request body", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", strings.NewReader("invalid"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handlePromtailPush(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestFormatLabels(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]string
		expected string
	}{
		{
			name:     "empty labels",
			input:    map[string]string{},
			expected: "{}",
		},
		{
			name: "single label",
			input: map[string]string{
				"job": "test",
			},
			expected: `{job="test"}`,
		},
		{
			name: "multiple labels",
			input: map[string]string{
				"job":      "test",
				"instance": "localhost",
				"level":    "info",
			},
			// Note: map iteration order is not guaranteed, so we test the content
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatLabels(tt.input)

			if tt.name == "multiple labels" {
				// For multiple labels, just check it contains all expected parts
				assert.Contains(t, result, `job="test"`)
				assert.Contains(t, result, `instance="localhost"`)
				assert.Contains(t, result, `level="info"`)
				assert.True(t, strings.HasPrefix(result, "{"))
				assert.True(t, strings.HasSuffix(result, "}"))
			} else {
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestPromtailServer_LogLevels(t *testing.T) {
	// Test that different log levels work correctly
	var logOutput bytes.Buffer
	logger := log.New(&logOutput, "", 0)

	config := DefaultServerConfig()
	config.Logger = logger
	config.LogLevel = LogLevelDebug

	server := NewPromtailServerWithConfig(config)

	// Test that debug logging works
	server.logDebug("test debug message")
	assert.Contains(t, logOutput.String(), "DEBUG: test debug message")

	// Reset buffer
	logOutput.Reset()

	// Test that trace logging doesn't work at debug level
	server.logTrace("test trace message")
	assert.Empty(t, logOutput.String())

	// Change to trace level
	config.LogLevel = LogLevelTrace
	server.logTrace("test trace message")
	assert.Contains(t, logOutput.String(), "TRACE: test trace message")
}

func TestPromtailServer_parseRequestBody(t *testing.T) {
	server := NewPromtailServerWithConfig(QuietServerConfig())

	t.Run("protobuf content type", func(t *testing.T) {
		pushReq := push.PushRequest{
			Streams: []push.Stream{
				{
					Labels: `{job="test"}`,
					Entries: []push.Entry{
						{Timestamp: time.Now(), Line: "test log"},
					},
				},
			},
		}

		data, err := proto.Marshal(&pushReq)
		require.NoError(t, err)

		streams, err := server.parseRequestBody(data, "application/x-protobuf")

		require.NoError(t, err)
		require.Len(t, streams, 1)
		assert.Equal(t, `{job="test"}`, streams[0].Labels)
	})

	t.Run("json content type", func(t *testing.T) {
		jsonData := `{
			"streams": [
				{
					"stream": {"job": "json-test"},
					"values": [["1640995200000000000", "json log"]]
				}
			]
		}`

		streams, err := server.parseRequestBody([]byte(jsonData), "application/json")

		require.NoError(t, err)
		require.Len(t, streams, 1)
		assert.Equal(t, `{job="json-test"}`, streams[0].Labels)
	})

	t.Run("default to json for unknown content type", func(t *testing.T) {
		jsonData := `{
			"streams": [
				{
					"stream": {"job": "default-test"},
					"values": [["1640995200000000000", "default log"]]
				}
			]
		}`

		streams, err := server.parseRequestBody([]byte(jsonData), "text/plain")

		require.NoError(t, err)
		require.Len(t, streams, 1)
		assert.Equal(t, `{job="default-test"}`, streams[0].Labels)
	})
}
