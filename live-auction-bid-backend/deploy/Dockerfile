FROM golang:1.25.12-alpine3.23@sha256:cc985ef6f9c3bf9ece7488129c9abe0a150388ccdfa428d886fc709dca0b230a AS builder

ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/auction-gateway ./app/auction/service/cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/auction-service ./app/auction/service/cmd/auction-service
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/auction-migrate ./app/auction/service/cmd/migrate
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/auction-outbox-relay ./app/auction/service/cmd/outbox-relay
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/auction-projector ./app/auction/service/cmd/projector
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/auction-projection-repair ./app/auction/service/cmd/projection-repair
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/auction-domain-relay ./app/auction/service/cmd/domain-relay
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/auction-enrichment-consumer ./app/auction/service/cmd/enrichment-consumer
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/auction-index-pgvector ./app/auction/service/cmd/index-pgvector
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/auction-index-es ./app/auction/service/cmd/index-es
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/auction-search-rebuild ./app/auction/service/cmd/search-rebuild
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/auction-search-reconciler ./app/auction/service/cmd/search-reconciler
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/auction-close-worker ./app/auction/service/cmd/close-worker

FROM alpine:3.20.10@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc

WORKDIR /app

COPY --from=builder /out/auction-gateway /app/auction-gateway
COPY --from=builder /out/auction-service /app/auction-service
COPY --from=builder /out/auction-migrate /app/auction-migrate
COPY --from=builder /out/auction-outbox-relay /app/auction-outbox-relay
COPY --from=builder /out/auction-projector /app/auction-projector
COPY --from=builder /out/auction-projection-repair /app/auction-projection-repair
COPY --from=builder /out/auction-domain-relay /app/auction-domain-relay
COPY --from=builder /out/auction-enrichment-consumer /app/auction-enrichment-consumer
COPY --from=builder /out/auction-index-pgvector /app/auction-index-pgvector
COPY --from=builder /out/auction-index-es /app/auction-index-es
COPY --from=builder /out/auction-search-rebuild /app/auction-search-rebuild
COPY --from=builder /out/auction-search-reconciler /app/auction-search-reconciler
COPY --from=builder /out/auction-close-worker /app/auction-close-worker
COPY deploy/elasticsearch/index-v1.json /app/elasticsearch/index-v1.json
COPY app/auction/service/configs/config.yaml /app/configs/config.yaml

ENV AUCTION_HTTP_ADDR=:18080
EXPOSE 18080 18081 18082 18083 18084 18085 18086 18087 18088 18089 18090 19090

ENTRYPOINT ["/app/auction-gateway"]
