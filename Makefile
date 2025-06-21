# Makefile
.PHONY: setup run load-test profile clean seed

setup:
	@echo "Setting up the environment..."
	docker-compose up -d postgres prometheus grafana
	@echo "Waiting for services to be ready..."
	sleep 10
	go mod tidy
	@echo "Setup complete!"

run:
	go run main.go

seed:
	@echo "Seeding database with test data..."
	@for i in $$(seq 1 100); do \
		curl -X POST http://localhost:8080/users \
		-H "Content-Type: application/json" \
		-d "{\"username\":\"user$$i\",\"email\":\"user$$i@example.com\",\"bio\":\"This is user $$i with some bio content that might be long enough to cause processing delays.\"}"; \
	done
	@echo "Database seeded with 100 users"

load-test-light:
	@echo "Running light load test..."
	hey -n 1000 -c 10 -m GET http://localhost:8080/users

load-test-medium:
	@echo "Running medium load test..."
	hey -n 5000 -c 50 -m GET http://localhost:8080/users

load-test-heavy:
	@echo "Running heavy load test..."
	hey -n 500000 -c 2000 -m GET http://localhost:8080/users

load-test-search:
	@echo "Running search load test..."
	hey -n 1000 -c 20 -m GET "http://localhost:8080/users/search?q=user"

load-test-mixed:
	@echo "Running mixed workload test..."
	@# Create users
	hey -n 500 -c 10 -m POST -H "Content-Type: application/json" \
		-d '{"username":"testuser","email":"test@example.com","bio":"Test bio"}' \
		http://localhost:8080/users &
	@# Get users
	hey -n 2000 -c 20 -m GET http://localhost:8080/users &
	@# Search users
	hey -n 500 -c 10 -m GET "http://localhost:8080/users/search?q=test" &
	wait

profile-cpu:
	@echo "Profiling CPU for 30 seconds..."
	go tool pprof -http=:8081 http://localhost:8080/debug/pprof/profile?seconds=30

profile-mem:
	@echo "Profiling memory..."
	go tool pprof -http=:8082 http://localhost:8080/debug/pprof/heap

profile-goroutines:
	@echo "Profiling goroutines..."
	go tool pprof -http=:8083 http://localhost:8080/debug/pprof/goroutine

# Text-based profiling commands (always work without Graphviz)
profile-cpu-text:
	@echo "CPU profiling (text mode)..."
	go tool pprof -text http://localhost:8080/debug/pprof/profile?seconds=30

profile-mem-text:
	@echo "Memory profiling (text mode)..."
	go tool pprof -text http://localhost:8080/debug/pprof/heap

profile-top-cpu:
	@echo "Top CPU functions..."
	go tool pprof -top http://localhost:8080/debug/pprof/profile?seconds=10

profile-top-mem:
	@echo "Top memory allocations..."
	go tool pprof -top http://localhost:8080/debug/pprof/heap


benchmark:
	go test -bench=. -benchmem

race-test:
	go run -race main.go

clean:
	docker-compose down -v
	docker system prune -f

help:
	@echo "Available targets:"
	@echo "  setup           - Setup the environment (DB, Prometheus, Grafana)"
	@echo "  run             - Run the application"
	@echo "  seed            - Seed database with test data"
	@echo "  load-test-*     - Run various load tests"
	@echo "  profile-*       - Run profiling tools"
	@echo "  benchmark       - Run Go benchmarks"
	@echo "  race-test       - Run with race detector"
	@echo "  clean           - Clean up everything"
