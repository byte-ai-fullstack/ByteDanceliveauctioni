.PHONY: api run run-auction-service run-enrichment-consumer run-index-pgvector run-index-es migrate-up migrate-status test test-core-coverage tidy zero-residue ws-load ws-reconnect-load bid-fleet-load search-load docker-up docker-down docker-logs

GO ?= go
PROTOC ?= /home/ye/OpenClaw/state/tools/protoc/bin/protoc
PROTOC_INC ?= third_party
PROTO_ROOT ?= api
PROTOC_PLUGIN_PATH ?= /home/ye/go/bin
COMPOSE ?= docker compose

api:
	PATH="$(PROTOC_PLUGIN_PATH):$$PATH" $(PROTOC) \
		--proto_path=$(PROTO_ROOT) \
		--proto_path=$(PROTOC_INC) \
		--go_out=paths=source_relative:$(PROTO_ROOT) \
		--go-grpc_out=paths=source_relative:$(PROTO_ROOT) \
		--go-http_out=paths=source_relative:$(PROTO_ROOT) \
		auction/service/v1/auction.proto \
		auction/service/v1/realtime.proto \
		auction/service/v1/user.proto \
		auction/service/v1/shop.proto

run:
	$(GO) run ./app/auction/service/cmd/server

run-auction-service:
	$(GO) run ./app/auction/service/cmd/auction-service

run-enrichment-consumer:
	$(GO) run ./app/auction/service/cmd/enrichment-consumer

run-index-pgvector:
	$(GO) run ./app/auction/service/cmd/index-pgvector

run-index-es:
	$(GO) run ./app/auction/service/cmd/index-es

migrate-up:
	$(GO) run ./app/auction/service/cmd/migrate up

migrate-status:
	$(GO) run ./app/auction/service/cmd/migrate status

test:
	$(GO) test ./...

test-core-coverage:
	bash scripts/check-core-coverage.sh

ws-load:
	AUCTION_WS_LOAD_CONNECTIONS="$${AUCTION_WS_LOAD_CONNECTIONS:-20000}" $(GO) test -run '^TestHubWebSocketLoad$$' -count=1 -timeout=10m -v ./app/auction/service/internal/realtime

ws-reconnect-load:
	AUCTION_WS_LOAD_CONNECTIONS="$${AUCTION_WS_LOAD_CONNECTIONS:-20000}" AUCTION_WS_LOAD_RECONNECT=1 $(GO) test -run '^TestHubWebSocketLoad$$' -count=1 -timeout=10m -v ./app/auction/service/internal/realtime

bid-fleet-load:
	node scripts/load-bid-fleet.mjs

search-load:
	node scripts/load-search-retrieval.mjs

tidy:
	$(GO) mod tidy

zero-residue:
	bash scripts/check-zero-residue.sh

docker-up:
	cd deploy && $(COMPOSE) up -d --build

docker-down:
	cd deploy && $(COMPOSE) down

docker-logs:
	cd deploy && $(COMPOSE) logs -f --tail=200
