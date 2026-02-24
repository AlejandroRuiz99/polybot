BINARY     := polybot
CMD        := ./cmd/scanner
BUILD_DIR  := bin
GOFLAGS    := -trimpath

.PHONY: all build test lint run run-once run-dry backtest paper paper-report live live-report live-stop clean tidy

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

paper: build
	./$(BUILD_DIR)/$(BINARY) --config config/config.yaml --paper --paper-capital 1000 --paper-markets 10 --verbose

paper-report: build
	./$(BUILD_DIR)/$(BINARY) --config config/config.yaml --paper-report --paper-capital 1000

live: build
	./$(BUILD_DIR)/$(BINARY) \
		--config config/config.yaml --live \
		--live-capital 7 --live-max-exposure 7 \
		--live-order-size 2 --live-markets 3 --verbose

live-report: build
	./$(BUILD_DIR)/$(BINARY) --config config/config.yaml --live-report

live-stop:
	touch STOP_LIVE

clean:
	rm -rf $(BUILD_DIR) coverage.out

tidy:
	go mod tidy
