#!/usr/bin/env bash
set -euo pipefail

raw_profile="${1:-coverage-raw.out}"
merged_profile="${2:-coverage.out}"

core_packages=(
  app/auction/service/internal/biz/auction
  app/auction/service/internal/eventcontract
  app/auction/service/internal/gateway
  app/auction/service/internal/kafkaclient
  app/auction/service/internal/projectiongate
  app/auction/service/internal/projectionrepair
  app/auction/service/internal/realtime
  app/auction/service/internal/runtimegeneration
  app/auction/service/internal/worker/closeworker
  app/auction/service/internal/worker/domainrelay
  app/auction/service/internal/worker/outboxrelay
  app/auction/service/internal/worker/projector
)

go test -race \
  -covermode=atomic \
  -coverpkg=./app/auction/service/internal/... \
  -coverprofile="${raw_profile}" \
  ./...

gate_args=(
  -profile "${raw_profile}"
  -output "${merged_profile}"
  -min 80
)
for package_path in "${core_packages[@]}"; do
  gate_args+=(-package "${package_path}")
done

go run ./tools/coveragegate "${gate_args[@]}"
