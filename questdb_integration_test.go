package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/grafana/loki/pkg/push"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestQuestDBIntegration(t *testing.T) {
	ctx := context.Background()
	
	// Start QuestDB container
	questdbContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "questdb/questdb:8.2.1",
			ExposedPorts: []string{
				"9009/tcp", // ILP port  
				"8812/tcp", // PostgreSQL wire protocol port
			},
			WaitingFor: wait.ForAll(
				wait.ForListeningPort("9009/tcp"),
				wait.ForListeningPort("8812/tcp"),
			).WithDeadline(30 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		questdbContainer.Terminate(ctx)
	})
	
	// Get ports
	ilpPort, err := questdbContainer.MappedPort(ctx, "9009")
	require.NoError(t, err)
	pgPort, err := questdbContainer.MappedPort(ctx, "8812")
	require.NoError(t, err)
	
	// Create ILP writer address for later use
	ilpAddr := fmt.Sprintf("tcp::addr=localhost:%s;", ilpPort.Port())
	
	// Connect to QuestDB via PostgreSQL wire protocol
	// QuestDB allows passwordless connections by default
	connStr := fmt.Sprintf("host=localhost port=%s user=admin password=quest dbname=qdb sslmode=disable", pgPort.Port())
	db, err := sql.Open("postgres", connStr)
	require.NoError(t, err)
	t.Cleanup(func() {
		db.Close()
	})
	
	// Wait for connection to be ready
	require.Eventually(t, func() bool {
		return db.Ping() == nil
	}, 30*time.Second, time.Second, "Database should be ready for connections")
	
	
	// Start promtail server with minimal logging for tests
	serverConfig := QuietServerConfig()
	serverConfig.Port = 0
	
	promtailServer := NewPromtailServerWithConfig(serverConfig)
	
	// Configure ILP writer with logging callbacks
	writerConfig := &ILPWriterConfig{
		OnSuccess: func(stream push.Stream) {
			t.Logf("Successfully wrote stream to QuestDB: %s", stream.Labels)
		},
		OnError: func(stream push.Stream, err error) {
			t.Logf("Error writing stream to QuestDB: %v", err)
		},
	}
	
	writer, err := NewILPWriterWithConfig(ilpAddr, writerConfig)
	require.NoError(t, err)
	t.Cleanup(func() {
		writer.Close()
	})
	
	// Add logging to debug
	receivedCount := 0
	promtailServer.SetHandler(func(stream push.Stream) {
		receivedCount++
		t.Logf("Received stream %d: %s", receivedCount, stream.Labels)
		writer.StreamHandler()(stream)
	})
	
	require.NoError(t, promtailServer.Start())
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		promtailServer.Stop(shutdownCtx)
	})
	
	// Start promtail container pointing to our server
	// Using localhost since promtail is running with host networking
	targetURL := fmt.Sprintf("http://localhost:%d", promtailServer.Port())
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
	
	promtailContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "grafana/promtail:2.9.0",
			ExposedPorts: []string{"9080/tcp"},
			Cmd:          []string{"-config.file=/etc/promtail/config.yml"},
			Files: []testcontainers.ContainerFile{
				{
					HostFilePath:      "./apache_logs.txt",
					ContainerFilePath: "/logs/apache.log",
				},
				{
					Reader:            strings.NewReader(promtailConf),
					ContainerFilePath: "/etc/promtail/config.yml",
				},
			},
			HostConfigModifier: func(hc *container.HostConfig) {
				hc.NetworkMode = "host"
			},
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		promtailContainer.Terminate(ctx)
	})
	
	// Query QuestDB to verify data
	t.Run("verify_log_count", func(t *testing.T) {
		var count int
		
		// Wait for data to be ingested
		require.Eventually(t, func() bool {
			err := db.QueryRow("SELECT count(*) FROM logs").Scan(&count)
			return err == nil && count > 0
		}, 15*time.Second, 500*time.Millisecond, "Should have received some logs in QuestDB")
		
		t.Logf("Total logs in QuestDB: %d", count)
	})
	
	t.Run("verify_log_content", func(t *testing.T) {
		rows, err := db.Query(`
			SELECT log, filename, ip, job, method, path, status, timestamp 
			FROM logs 
			ORDER BY timestamp DESC 
			LIMIT 10
		`)
		require.NoError(t, err)
		defer rows.Close()
		
		logCount := 0
		for rows.Next() {
			var log, filename, ip, job, method, path, status string
			var timestamp time.Time
			
			err := rows.Scan(&log, &filename, &ip, &job, &method, &path, &status, &timestamp)
			require.NoError(t, err)
			
			t.Logf("\nLog entry:")
			t.Logf("  Timestamp: %s", timestamp)
			t.Logf("  IP: %s", ip)
			t.Logf("  Method: %s", method)
			t.Logf("  Path: %s", path)
			t.Logf("  Status: %s", status)
			t.Logf("  Log: %s", log[:min(100, len(log))])
			
			// Verify fields are populated
			require.NotEmpty(t, log)
			require.NotEmpty(t, ip)
			require.NotEmpty(t, method)
			require.NotEmpty(t, path)
			require.NotEmpty(t, status)
			require.Equal(t, "apache", job)
			require.Equal(t, "/logs/apache.log", filename)
			
			logCount++
		}
		require.Greater(t, logCount, 0, "Should have retrieved some logs")
	})
	
	t.Run("verify_aggregation", func(t *testing.T) {
		rows, err := db.Query(`
			SELECT method, status, count(*) as cnt 
			FROM logs 
			GROUP BY method, status 
			ORDER BY cnt DESC 
			LIMIT 5
		`)
		require.NoError(t, err)
		defer rows.Close()
		
		t.Logf("\nTop method/status combinations:")
		for rows.Next() {
			var method, status string
			var count int
			
			err := rows.Scan(&method, &status, &count)
			require.NoError(t, err)
			t.Logf("  %s %s: %d requests", method, status, count)
		}
	})
}