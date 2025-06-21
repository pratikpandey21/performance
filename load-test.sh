#!/bin/bash

echo "Starting comprehensive load test..."

# Function to run load test and capture metrics
run_test() {
    local name=$1
    local command=$2
    echo "Running $name..."
    echo "=========================="
    eval $command
    echo ""
    sleep 5
}

# Warm up
echo "Warming up the service..."
curl -s http://localhost:8080/health > /dev/null

# Sequential tests
run_test "Light GET load" "hey -n 1000 -c 10 http://localhost:8080/users"
run_test "Medium GET load" "hey -n 5000 -c 50 http://localhost:8080/users"
run_test "Individual user fetch" "hey -n 1000 -c 20 http://localhost:8080/users/1"
run_test "Search functionality" "hey -n 500 -c 10 'http://localhost:8080/users/search?q=user'"

# Mixed workload
echo "Running mixed workload (30 seconds)..."
timeout 30s bash -c '
while true; do
    curl -s -X POST http://localhost:8080/users \
      -H "Content-Type: application/json" \
      -d "{\"username\":\"user$(date +%s)\",\"email\":\"user$(date +%s)@example.com\",\"bio\":\"Test bio content\"}" &
    curl -s http://localhost:8080/users > /dev/null &
    curl -s "http://localhost:8080/users/search?q=test" > /dev/null &
    sleep 0.1
done
wait
'

echo "Load test completed! Check Grafana dashboard at http://localhost:3000"