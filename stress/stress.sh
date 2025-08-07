#!/bin/bash

# Simple stress testing script for promtail2ilp
# Usage: ./stress.sh [test_type]
# test_type can be: load, concurrent, payloads, protobuf, all

set -e

TEST_TYPE=${1:-all}

echo "🚀 Promtail2ILP Stress Testing"
echo "==============================="

case "$TEST_TYPE" in
    "load"|"http")
        echo "Running HTTP load test..."
        go test -v -run="TestStress_HTTPLoad" -timeout=5m ./...
        ;;
    "concurrent"|"conn")
        echo "Running concurrent connections test..."
        go test -v -run="TestStress_ConcurrentConnections" -timeout=5m ./...
        ;;
    "payloads"|"large")
        echo "Running large payloads test..."
        go test -v -run="TestStress_LargePayloads" -timeout=5m ./...
        ;;
    "protobuf"|"proto")
        echo "Running protobuf load test..."
        go test -v -run="TestStress_ProtobufLoad" -timeout=5m ./...
        ;;
    "memory"|"mem")
        echo "Running INSANE memory pressure test..."
        go test -v -run="TestStress_MemoryPressure" -timeout=10m ./...
        ;;
    "sustained"|"endurance")
        echo "Running sustained load test (30 seconds of pain!)..."
        go test -v -run="TestStress_SustainedLoad" -timeout=2m ./...
        ;;
    "mixed"|"chaos")
        echo "Running mixed workload chaos test..."
        go test -v -run="TestStress_MixedWorkload" -timeout=5m ./...
        ;;
    "extreme"|"hell")
        echo "🔥🔥🔥 RUNNING ALL EXTREME STRESS TESTS - PREPARE FOR CHAOS! 🔥🔥🔥"
        echo ""
        echo "💥 Memory Pressure Test"
        echo "----------------------"
        go test -v -run="TestStress_MemoryPressure" -timeout=10m ./...
        echo ""
        echo "⏰ Sustained Load Test"
        echo "---------------------"
        go test -v -run="TestStress_SustainedLoad" -timeout=2m ./...
        echo ""
        echo "🌪️  Mixed Workload Chaos Test"
        echo "-----------------------------"
        go test -v -run="TestStress_MixedWorkload" -timeout=5m ./...
        echo ""
        echo "🚀 EXTREME HTTP Load Test"
        echo "-------------------------"
        go test -v -run="TestStress_HTTPLoad" -timeout=10m ./...
        echo ""
        echo "⚡ EXTREME Concurrent Connections"
        echo "--------------------------------"
        go test -v -run="TestStress_ConcurrentConnections" -timeout=10m ./...
        echo ""
        echo "📦 MEGA Payloads Test"
        echo "--------------------"
        go test -v -run="TestStress_LargePayloads" -timeout=15m ./...
        ;;
    "all")
        echo "Running all stress tests..."
        echo ""
        echo "📊 HTTP Load Test"
        echo "-----------------"
        go test -v -run="TestStress_HTTPLoad" -timeout=5m ./...
        echo ""
        echo "🔗 Concurrent Connections Test"
        echo "------------------------------"
        go test -v -run="TestStress_ConcurrentConnections" -timeout=5m ./...
        echo ""
        echo "📦 Large Payloads Test"
        echo "---------------------"
        go test -v -run="TestStress_LargePayloads" -timeout=5m ./...
        echo ""
        echo "🔧 Protobuf Load Test"
        echo "--------------------"
        go test -v -run="TestStress_ProtobufLoad" -timeout=5m ./...
        ;;
    "race")
        echo "Running all stress tests with race detection..."
        go test -v -race -run="TestStress" -timeout=10m ./...
        ;;
    *)
        echo "Usage: $0 [test_type]"
        echo ""
        echo "Available test types:"
        echo "  load       - HTTP load test (200 workers × 100 requests = 20K requests!)"
        echo "  concurrent - Concurrent connections test (500 simultaneous connections!)"
        echo "  payloads   - Large payloads test (up to 500 streams × 5K entries each!)"
        echo "  protobuf   - Protobuf format load test (1000 requests!)"
        echo "  memory     - Memory pressure test (INSANE payload sizes!)"
        echo "  sustained  - Sustained load test (30 seconds of continuous stress!)"
        echo "  mixed      - Mixed workload chaos (JSON + Protobuf simultaneously!)"
        echo "  extreme    - ALL THE EXTREME TESTS! 🔥"
        echo "  race       - All tests with race detection"
        echo "  all        - Run all stress tests (default)"
        echo ""
        echo "Examples:"
        echo "  $0 load"
        echo "  $0 concurrent"
        echo "  $0 all"
        exit 1
        ;;
esac

echo ""
echo "✅ Stress testing completed!"