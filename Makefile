.PHONY: test bench build run clean

test:
	go test ./... -v -count=1 -timeout 60s

bench:
	go test ./internal/bench/ -v -run TestLatencyOverhead -timeout 30s
	go test ./internal/bench/ -bench=. -benchmem -timeout 30s

build:
	go build -o bin/gateway ./cmd/gateway

run: build
	./bin/gateway -config config.yaml

clean:
	rm -rf bin/
