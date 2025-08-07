package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/grafana/loki/pkg/push"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStress_HTTPLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	server := NewPromtailServerWithConfig(QuietServerConfig())

	var totalReceived int64
	server.SetHandler(func(stream push.Stream) {
		atomic.AddInt64(&totalReceived, int64(len(stream.Entries)))
	})

	require.NoError(t, server.Start())
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Stop(shutdownCtx)
	})

	// Test parameters - EXTREME LOAD! 🔥
	const (
		numWorkers   = 200
		requestsPerWorker = 100
		totalRequests = numWorkers * requestsPerWorker
	)

	targetURL := fmt.Sprintf("http://localhost:%d", server.Port())

	t.Logf("Starting load test: %d workers × %d requests = %d total requests", 
		numWorkers, requestsPerWorker, totalRequests)

	var wg sync.WaitGroup
	var successCount int64
	var errorCount int64
	startTime := time.Now()

	// Launch workers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			
			client := &http.Client{Timeout: 10 * time.Second}
			
			for j := 0; j < requestsPerWorker; j++ {
				// Customize payload with worker ID
				customPayload := fmt.Sprintf(`{
					"streams": [
						{
							"stream": {"job": "stress-test", "worker": "%d"},
							"values": [
								["%d", "worker %d request %d log line 1"],
								["%d", "worker %d request %d log line 2"]
							]
						}
					]
				}`, workerID, time.Now().UnixNano(), workerID, j, time.Now().UnixNano()+1, workerID, j)

				resp, err := client.Post(targetURL, "application/json", bytes.NewReader([]byte(customPayload)))
				if err != nil {
					atomic.AddInt64(&errorCount, 1)
					t.Logf("Worker %d request %d failed: %v", workerID, j, err)
					continue
				}
				resp.Body.Close()

				if resp.StatusCode == http.StatusNoContent {
					atomic.AddInt64(&successCount, 1)
				} else {
					atomic.AddInt64(&errorCount, 1)
					t.Logf("Worker %d request %d got status %d", workerID, j, resp.StatusCode)
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(startTime)

	// Results
	t.Logf("Load test completed in %v", duration)
	t.Logf("Successful requests: %d/%d (%.1f%%)", 
		successCount, totalRequests, float64(successCount)/float64(totalRequests)*100)
	t.Logf("Failed requests: %d", errorCount)
	t.Logf("Requests per second: %.1f", float64(totalRequests)/duration.Seconds())
	t.Logf("Total log entries received: %d", atomic.LoadInt64(&totalReceived))

	// Assertions
	assert.Equal(t, int64(totalRequests), successCount+errorCount, "All requests should be accounted for")
	assert.GreaterOrEqual(t, successCount, int64(totalRequests*0.95), "At least 95% of requests should succeed")
	assert.Greater(t, atomic.LoadInt64(&totalReceived), int64(0), "Should have received some log entries")
}

func TestStress_ConcurrentConnections(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	server := NewPromtailServerWithConfig(QuietServerConfig())

	var totalStreams int64
	server.SetHandler(func(stream push.Stream) {
		atomic.AddInt64(&totalStreams, 1)
	})

	require.NoError(t, server.Start())
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Stop(shutdownCtx)
	})

	// Test INSANE simultaneous connections 🚀
	const numConnections = 500
	targetURL := fmt.Sprintf("http://localhost:%d", server.Port())

	t.Logf("Testing %d concurrent connections", numConnections)

	var wg sync.WaitGroup
	var successCount int64
	startTime := time.Now()

	// Create a channel to control connection timing
	startChan := make(chan struct{})

	for i := 0; i < numConnections; i++ {
		wg.Add(1)
		go func(connID int) {
			defer wg.Done()
			
			// Wait for start signal
			<-startChan
			
			client := &http.Client{Timeout: 5 * time.Second}
			payload := fmt.Sprintf(`{
				"streams": [
					{
						"stream": {"job": "concurrent-test", "conn": "%d"},
						"values": [["1640995200000000000", "concurrent connection %d"]]
					}
				]
			}`, connID, connID)

			resp, err := client.Post(targetURL, "application/json", bytes.NewReader([]byte(payload)))
			if err != nil {
				t.Logf("Connection %d failed: %v", connID, err)
				return
			}
			resp.Body.Close()

			if resp.StatusCode == http.StatusNoContent {
				atomic.AddInt64(&successCount, 1)
			}
		}(i)
	}

	// Start all connections simultaneously
	close(startChan)
	wg.Wait()
	
	duration := time.Since(startTime)

	t.Logf("Concurrent connection test completed in %v", duration)
	t.Logf("Successful connections: %d/%d", successCount, numConnections)
	t.Logf("Total streams processed: %d", atomic.LoadInt64(&totalStreams))

	assert.GreaterOrEqual(t, successCount, int64(numConnections*0.9), "At least 90% of connections should succeed")
}

func TestStress_LargePayloads(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	server := NewPromtailServerWithConfig(QuietServerConfig())

	var totalEntries int64
	server.SetHandler(func(stream push.Stream) {
		atomic.AddInt64(&totalEntries, int64(len(stream.Entries)))
	})

	require.NoError(t, server.Start())
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Stop(shutdownCtx)
	})

	targetURL := fmt.Sprintf("http://localhost:%d", server.Port())

	// Test MASSIVE payload sizes 💥
	testCases := []struct {
		name           string
		numStreams     int
		entriesPerStream int
		logLineSize    int
	}{
		{"Small batch", 10, 50, 200},
		{"Medium batch", 25, 100, 1000},
		{"Large batch", 50, 500, 5000},
		{"Extra large batch", 100, 1000, 10000},
		{"MEGA batch", 200, 2000, 20000},
		{"ULTRA batch", 500, 5000, 50000},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset counter
			atomic.StoreInt64(&totalEntries, 0)

			// Create large payload
			payload := createLargeJSONPayload(tc.numStreams, tc.entriesPerStream, tc.logLineSize)
			payloadSize := len(payload)

			t.Logf("Testing payload: %d streams × %d entries × %d chars = %d bytes", 
				tc.numStreams, tc.entriesPerStream, tc.logLineSize, payloadSize)

			startTime := time.Now()
			
			client := &http.Client{Timeout: 30 * time.Second}
			resp, err := client.Post(targetURL, "application/json", bytes.NewReader(payload))
			
			duration := time.Since(startTime)
			
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusNoContent, resp.StatusCode)
			
			// Give a moment for the handler to process
			time.Sleep(10 * time.Millisecond)
			
			expectedEntries := int64(tc.numStreams * tc.entriesPerStream)
			actualEntries := atomic.LoadInt64(&totalEntries)
			
			t.Logf("Processed %d KB in %v (%.1f KB/s)", 
				payloadSize/1024, duration, float64(payloadSize/1024)/duration.Seconds())
			t.Logf("Expected %d entries, got %d", expectedEntries, actualEntries)
			
			assert.Equal(t, expectedEntries, actualEntries)
		})
	}
}

func TestStress_ProtobufLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	server := NewPromtailServerWithConfig(QuietServerConfig())

	var totalEntries int64
	server.SetHandler(func(stream push.Stream) {
		atomic.AddInt64(&totalEntries, int64(len(stream.Entries)))
	})

	require.NoError(t, server.Start())
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Stop(shutdownCtx)
	})

	const numRequests = 1000 // PROTOBUF MAYHEM! 🔥
	targetURL := fmt.Sprintf("http://localhost:%d", server.Port())

	t.Logf("Testing %d protobuf requests", numRequests)

	var wg sync.WaitGroup
	var successCount int64
	startTime := time.Now()

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(reqID int) {
			defer wg.Done()

			// Create protobuf payload
			pushReq := push.PushRequest{
				Streams: []push.Stream{
					{
						Labels: fmt.Sprintf(`{job="protobuf-stress", req="%d"}`, reqID),
						Entries: []push.Entry{
							{
								Timestamp: time.Now(),
								Line:      fmt.Sprintf("protobuf stress test request %d line 1", reqID),
							},
							{
								Timestamp: time.Now().Add(time.Millisecond),
								Line:      fmt.Sprintf("protobuf stress test request %d line 2", reqID),
							},
						},
					},
				},
			}

			data, err := proto.Marshal(&pushReq)
			if err != nil {
				t.Logf("Failed to marshal protobuf for request %d: %v", reqID, err)
				return
			}

			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Post(targetURL, "application/x-protobuf", bytes.NewReader(data))
			if err != nil {
				t.Logf("Request %d failed: %v", reqID, err)
				return
			}
			resp.Body.Close()

			if resp.StatusCode == http.StatusNoContent {
				atomic.AddInt64(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(startTime)

	t.Logf("Protobuf load test completed in %v", duration)
	t.Logf("Successful requests: %d/%d", successCount, numRequests)
	t.Logf("Requests per second: %.1f", float64(numRequests)/duration.Seconds())
	t.Logf("Total entries processed: %d", atomic.LoadInt64(&totalEntries))

	assert.Equal(t, int64(numRequests), successCount)
	assert.Equal(t, int64(numRequests*2), atomic.LoadInt64(&totalEntries)) // 2 entries per request
}

func TestStress_MemoryPressure(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	server := NewPromtailServerWithConfig(QuietServerConfig())

	var totalEntries int64
	var totalBytes int64
	server.SetHandler(func(stream push.Stream) {
		atomic.AddInt64(&totalEntries, int64(len(stream.Entries)))
		for _, entry := range stream.Entries {
			atomic.AddInt64(&totalBytes, int64(len(entry.Line)))
		}
	})

	require.NoError(t, server.Start())
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Stop(shutdownCtx)
	})

	targetURL := fmt.Sprintf("http://localhost:%d", server.Port())

	// INSANE memory pressure test - multiple huge payloads simultaneously!
	const (
		numGoroutines = 50
		streamsPerRequest = 100
		entriesPerStream = 500
		logLineSize = 10000 // 10KB per log line!
	)

	t.Logf("🔥 MEMORY PRESSURE TEST: %d goroutines × %d streams × %d entries × %dKB = ~%dGB total", 
		numGoroutines, streamsPerRequest, entriesPerStream, logLineSize/1024,
		(numGoroutines*streamsPerRequest*entriesPerStream*logLineSize)/(1024*1024*1024))

	var wg sync.WaitGroup
	var successCount int64
	startTime := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			payload := createLargeJSONPayload(streamsPerRequest, entriesPerStream, logLineSize)
			
			client := &http.Client{Timeout: 60 * time.Second}
			resp, err := client.Post(targetURL, "application/json", bytes.NewReader(payload))
			if err != nil {
				t.Logf("Goroutine %d failed: %v", goroutineID, err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusNoContent {
				atomic.AddInt64(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(startTime)

	// Give time for all handlers to process
	time.Sleep(100 * time.Millisecond)

	totalEntriesProcessed := atomic.LoadInt64(&totalEntries)
	totalBytesProcessed := atomic.LoadInt64(&totalBytes)

	t.Logf("🚀 Memory pressure test completed in %v", duration)
	t.Logf("📈 Successful requests: %d/%d", successCount, numGoroutines)
	t.Logf("📊 Total entries processed: %d", totalEntriesProcessed)
	t.Logf("💾 Total data processed: %.2f MB", float64(totalBytesProcessed)/(1024*1024))
	t.Logf("⚡ Processing rate: %.2f MB/s", float64(totalBytesProcessed)/(1024*1024)/duration.Seconds())

	expectedEntries := int64(numGoroutines * streamsPerRequest * entriesPerStream)
	assert.GreaterOrEqual(t, successCount, int64(numGoroutines*0.8), "At least 80% of requests should succeed under memory pressure")
	assert.GreaterOrEqual(t, totalEntriesProcessed, expectedEntries*8/10, "Should process at least 80% of entries under memory pressure")
}

func TestStress_SustainedLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	server := NewPromtailServerWithConfig(QuietServerConfig())

	var totalRequests int64
	var totalEntries int64
	server.SetHandler(func(stream push.Stream) {
		atomic.AddInt64(&totalRequests, 1)
		atomic.AddInt64(&totalEntries, int64(len(stream.Entries)))
	})

	require.NoError(t, server.Start())
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Stop(shutdownCtx)
	})

	targetURL := fmt.Sprintf("http://localhost:%d", server.Port())

	// SUSTAINED HIGH LOAD for 30 seconds! 💪
	const (
		testDuration = 30 * time.Second
		numWorkers = 100
		requestInterval = 10 * time.Millisecond // One request every 10ms per worker
	)

	t.Logf("🔥 SUSTAINED LOAD TEST: %d workers for %v (one request every %v per worker)", 
		numWorkers, testDuration, requestInterval)

	ctx, cancel := context.WithTimeout(context.Background(), testDuration)
	defer cancel()

	var wg sync.WaitGroup
	var successCount int64
	var errorCount int64
	startTime := time.Now()

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			
			client := &http.Client{Timeout: 5 * time.Second}
			ticker := time.NewTicker(requestInterval)
			defer ticker.Stop()

			requestNum := 0
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					requestNum++
					payload := fmt.Sprintf(`{
						"streams": [
							{
								"stream": {"job": "sustained-test", "worker": "%d"},
								"values": [
									["%d", "sustained worker %d request %d - high frequency load test"],
									["%d", "sustained worker %d request %d - continuous stress test"]
								]
							}
						]
					}`, workerID, time.Now().UnixNano(), workerID, requestNum, time.Now().UnixNano()+1, workerID, requestNum)

					resp, err := client.Post(targetURL, "application/json", bytes.NewReader([]byte(payload)))
					if err != nil {
						atomic.AddInt64(&errorCount, 1)
						continue
					}
					resp.Body.Close()

					if resp.StatusCode == http.StatusNoContent {
						atomic.AddInt64(&successCount, 1)
					} else {
						atomic.AddInt64(&errorCount, 1)
					}
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(startTime)

	// Give time for handlers to finish
	time.Sleep(100 * time.Millisecond)

	t.Logf("⏱️  Sustained load test completed in %v", duration)
	t.Logf("✅ Successful requests: %d", successCount)
	t.Logf("❌ Failed requests: %d", errorCount)
	t.Logf("📊 Total requests processed by server: %d", atomic.LoadInt64(&totalRequests))
	t.Logf("📈 Average requests per second: %.1f", float64(successCount)/duration.Seconds())
	t.Logf("🔢 Total log entries processed: %d", atomic.LoadInt64(&totalEntries))
	
	totalRequestsMade := successCount + errorCount
	successRate := float64(successCount) / float64(totalRequestsMade) * 100
	t.Logf("🎯 Success rate: %.1f%%", successRate)

	assert.Greater(t, successCount, int64(0), "Should have some successful requests")
	assert.GreaterOrEqual(t, successRate, 90.0, "Should maintain at least 90% success rate under sustained load")
}

func TestStress_MixedWorkload(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	server := NewPromtailServerWithConfig(QuietServerConfig())

	var jsonRequests int64
	var protobufRequests int64
	var totalEntries int64
	server.SetHandler(func(stream push.Stream) {
		atomic.AddInt64(&totalEntries, int64(len(stream.Entries)))
		// Detect format by labels (crude but works for testing)
		if bytes.Contains([]byte(stream.Labels), []byte("json")) {
			atomic.AddInt64(&jsonRequests, 1)
		} else if bytes.Contains([]byte(stream.Labels), []byte("proto")) {
			atomic.AddInt64(&protobufRequests, 1)
		}
	})

	require.NoError(t, server.Start())
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Stop(shutdownCtx)
	})

	targetURL := fmt.Sprintf("http://localhost:%d", server.Port())

	// MIXED WORKLOAD CHAOS! 🌪️
	const (
		numJSONWorkers = 75
		numProtobufWorkers = 25
		requestsPerWorker = 50
	)

	t.Logf("🌪️  MIXED WORKLOAD TEST: %d JSON workers + %d protobuf workers × %d requests each", 
		numJSONWorkers, numProtobufWorkers, requestsPerWorker)

	var wg sync.WaitGroup
	var successCount int64
	startTime := time.Now()

	// JSON workers
	for i := 0; i < numJSONWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			
			client := &http.Client{Timeout: 10 * time.Second}
			
			for j := 0; j < requestsPerWorker; j++ {
				payload := fmt.Sprintf(`{
					"streams": [
						{
							"stream": {"job": "mixed-json", "worker": "%d", "format": "json"},
							"values": [
								["%d", "JSON mixed workload test worker %d request %d - causing chaos!"],
								["%d", "JSON mixed workload test worker %d request %d - maximum stress!"]
							]
						}
					]
				}`, workerID, time.Now().UnixNano(), workerID, j, time.Now().UnixNano()+1, workerID, j)

				resp, err := client.Post(targetURL, "application/json", bytes.NewReader([]byte(payload)))
				if err != nil {
					continue
				}
				resp.Body.Close()

				if resp.StatusCode == http.StatusNoContent {
					atomic.AddInt64(&successCount, 1)
				}
			}
		}(i)
	}

	// Protobuf workers
	for i := 0; i < numProtobufWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			
			client := &http.Client{Timeout: 10 * time.Second}
			
			for j := 0; j < requestsPerWorker; j++ {
				pushReq := push.PushRequest{
					Streams: []push.Stream{
						{
							Labels: fmt.Sprintf(`{job="mixed-proto", worker="%d", format="protobuf"}`, workerID),
							Entries: []push.Entry{
								{
									Timestamp: time.Now(),
									Line:      fmt.Sprintf("PROTOBUF mixed workload worker %d request %d - binary chaos!", workerID, j),
								},
								{
									Timestamp: time.Now().Add(time.Millisecond),
									Line:      fmt.Sprintf("PROTOBUF mixed workload worker %d request %d - binary mayhem!", workerID, j),
								},
							},
						},
					},
				}

				data, err := proto.Marshal(&pushReq)
				if err != nil {
					continue
				}

				resp, err := client.Post(targetURL, "application/x-protobuf", bytes.NewReader(data))
				if err != nil {
					continue
				}
				resp.Body.Close()

				if resp.StatusCode == http.StatusNoContent {
					atomic.AddInt64(&successCount, 1)
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(startTime)

	// Give time for handlers
	time.Sleep(100 * time.Millisecond)

	expectedTotal := numJSONWorkers*requestsPerWorker + numProtobufWorkers*requestsPerWorker

	t.Logf("🎭 Mixed workload test completed in %v", duration)
	t.Logf("📊 Total successful requests: %d/%d", successCount, expectedTotal)
	t.Logf("📝 JSON streams processed: %d", atomic.LoadInt64(&jsonRequests))
	t.Logf("🔧 Protobuf streams processed: %d", atomic.LoadInt64(&protobufRequests))
	t.Logf("📈 Total entries processed: %d", atomic.LoadInt64(&totalEntries))
	t.Logf("⚡ Mixed requests per second: %.1f", float64(successCount)/duration.Seconds())

	assert.GreaterOrEqual(t, successCount, int64(float64(expectedTotal)*0.9), "Should handle 90%+ of mixed workload")
	assert.Greater(t, atomic.LoadInt64(&jsonRequests), int64(0), "Should process JSON requests")
	assert.Greater(t, atomic.LoadInt64(&protobufRequests), int64(0), "Should process protobuf requests")
}

// Helper function to create large JSON payloads
func createLargeJSONPayload(numStreams, entriesPerStream, logLineSize int) []byte {
	type jsonStream struct {
		Stream map[string]string `json:"stream"`
		Values [][]string        `json:"values"`
	}

	type jsonPayload struct {
		Streams []jsonStream `json:"streams"`
	}

	// Create a large log line
	logLine := fmt.Sprintf("Large log entry with %d characters: %s", 
		logLineSize, randomString(logLineSize-50))

	payload := jsonPayload{
		Streams: make([]jsonStream, numStreams),
	}

	for i := 0; i < numStreams; i++ {
		stream := jsonStream{
			Stream: map[string]string{
				"job":    "large-payload-test",
				"stream": fmt.Sprintf("stream-%d", i),
			},
			Values: make([][]string, entriesPerStream),
		}

		for j := 0; j < entriesPerStream; j++ {
			timestamp := fmt.Sprintf("%d", time.Now().UnixNano()+int64(j))
			line := fmt.Sprintf("%s (stream %d, entry %d)", logLine, i, j)
			stream.Values[j] = []string{timestamp, line}
		}

		payload.Streams[i] = stream
	}

	data, _ := json.Marshal(payload)
	return data
}

// Helper function to generate random strings
func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 "
	if length <= 0 {
		return ""
	}
	
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[i%len(charset)]
	}
	return string(result)
}