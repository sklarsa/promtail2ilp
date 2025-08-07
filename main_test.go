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
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
)

func TestBasicE2E(t *testing.T) {
	ctx := context.Background()
	targetURL := "http://localhost:9999"
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
	time.Sleep(time.Second * 10)
	t.Cleanup(func() {
		ctr.Terminate(ctx)
	})
}
