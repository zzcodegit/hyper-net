APP_NAME := hypernet-node
BIN_DIR := bin

.PHONY: all build run test docker-build

all: build

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/$(APP_NAME) ./cmd/node

run:
	go run ./cmd/node

test:
	go test ./...

docker-build:
	docker build -t $(APP_NAME) .

