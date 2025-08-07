package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/grafana/loki/pkg/push"
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
