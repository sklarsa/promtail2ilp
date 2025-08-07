# promtail2ilp

A lightweight HTTP server that receives log streams from [Grafana Promtail](https://grafana.com/docs/loki/latest/clients/promtail/) and forwards them to [QuestDB](https://questdb.io/) using the Influx Line Protocol (ILP).


## Warning (from a Human, I swear!)

This is just about 100% vibe-coded by Claude. Use at your own risk!

## Features

- **Multiple input formats**: JSON and Protocol Buffer (Loki's native format)
- **Compression support**: gzip and snappy decompression
- **Configurable logging**: Error, info, debug, and trace levels
- **Custom handlers**: Process log streams with your own logic
- **Graceful shutdown**: Proper HTTP server lifecycle management
- **Automatic schema**: QuestDB creates tables automatically via ILP

## Quick Start

### Build and Run

```bash
# Build the binary
go build -o promtail2ilp

# Run with default settings (port 9999)
./promtail2ilp

# Run with custom port and debug logging
./promtail2ilp -port 8080 -log-level debug
```

### Command Line Options

```
-port int
    HTTP port to listen on (default 9999)
-log-level string
    Log level: error, info, debug, trace (default "info")  
-help
    Show help message
```

### Configure Promtail

Point Promtail to your server by adding a client configuration:

```yaml
# promtail-config.yml
server:
  http_listen_port: 9080

clients:
  - url: http://localhost:9999  # Your promtail2ilp server

scrape_configs:
  - job_name: my-app
    static_configs:
      - targets:
          - localhost
        labels:
          job: my-app
          __path__: /var/log/my-app/*.log
```

## Usage as Library

```go
package main

import (
    "context"
    "log"
    "time"
)

func main() {
    // Create server with custom configuration
    config := DefaultServerConfig()
    config.Port = 8080
    config.LogLevel = LogLevelDebug
    
    server := NewPromtailServerWithConfig(config)
    
    // Set custom handler to process log streams
    server.SetHandler(func(stream push.Stream) {
        log.Printf("Received %d log entries from %s", 
            len(stream.Entries), stream.Labels)
        
        // Forward to QuestDB, process, etc.
        // Your custom logic here
    })
    
    // Start server
    if err := server.Start(); err != nil {
        log.Fatal(err)
    }
    
    // Graceful shutdown
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    server.Stop(ctx)
}
```

## QuestDB Integration

The server is designed to work with QuestDB's automatic table creation via ILP. When log streams are processed, they can be written directly to QuestDB without manually creating schemas.

Example log table structure automatically created:
- `log` (STRING): The log message
- `timestamp` (TIMESTAMP): Log entry timestamp  
- Dynamic label columns based on Promtail labels (job, filename, etc.)

## Development

### Run Tests

```bash
# Unit tests only
go test -short

# All tests (requires Docker for integration tests)
go test

# Specific test functions
go test -run TestPromtailServer_parseJSON
```

### Build with Make

```bash
# View available targets
make help

# Build binary
make build

# Run tests
make test

# Run with race detection
make test-race

# Generate test coverage
make test-coverage
```

## License

MIT License - see [LICENSE](LICENSE) file for details.