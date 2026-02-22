BINARY     := polybot
CMD        := ./cmd/scanner
BUILD_DIR  := bin
GOFLAGS    := -trimpath

.PHONY: all build test lint run run-once run-dry backtest clean tidy

all: build

build:
	go build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY) $(CMD)

test:
	go test ./... -count=1 -race -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1

lint:
	golangci-lint run ./...

run: build
	./$(BUILD_DIR)/$(BINARY) --config config/config.yaml

run-once: build
	./$(BUILD_DIR)/$(BINARY) --config config/config.yaml --once

run-dry: build
	./$(BUILD_DIR)/$(BINARY) --config config/config.yaml --once --dry-run

backtest: build
	./$(BUILD_DIR)/$(BINARY) --config config/config.yaml --backtest --verbose

clean:
	rm -rf $(BUILD_DIR) coverage.out

tidy:
	go mod tidy
