COMPOSE  := docker compose -f deploy/docker-compose.yml
SERVICES := controlplane executor router

# go build writes exactly the name it is given; Windows needs the .exe suffix.
ifeq ($(OS),Windows_NT)
EXE := .exe
endif

.PHONY: help up down reset logs ps run-controlplane run-executor run-router build test vet fmt tidy

help:
	@echo "up | down | reset | logs | ps          infra (Postgres, Redis, MinIO)"
	@echo "run-controlplane | run-executor | run-router   start one service"
	@echo "build | test | vet | fmt | tidy          Go tooling"

## --- infrastructure ---

up:
	$(COMPOSE) up -d --wait

down:
	$(COMPOSE) down

# Deletes the volumes too: every row, key and object is gone.
reset:
	$(COMPOSE) down -v

logs:
	$(COMPOSE) logs -f

ps:
	$(COMPOSE) ps

## --- services (one per terminal) ---

run-controlplane:
	go run ./cmd/controlplane

run-executor:
	go run ./cmd/executor

run-router:
	go run ./cmd/router

## --- Go tooling ---

build:
	$(foreach s,$(SERVICES),go build -o bin/$(s)$(EXE) ./cmd/$(s) &&) true

test:
	go test ./... -race -count=1

vet:
	go vet ./...

fmt:
	gofmt -s -w .

tidy:
	go mod tidy

.DEFAULT_GOAL := help
