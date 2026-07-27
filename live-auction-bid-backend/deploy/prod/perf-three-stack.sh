#!/usr/bin/env bash
set -euo pipefail

SERVER_DIR="${SERVER_DIR:-/opt/live-auction}"
BASE_COMPOSE="${BASE_COMPOSE:-$SERVER_DIR/docker-compose.yml}"
PERF_COMPOSE="${PERF_COMPOSE:-$SERVER_DIR/backend/deploy/prod/docker-compose.perf-3x.yml}"
SINGLE_NGINX="${SINGLE_NGINX:-$SERVER_DIR/live-auction.nginx.conf}"
PERF_NGINX="${PERF_NGINX:-$SERVER_DIR/backend/deploy/prod/live-auction.perf-3x.nginx.conf}"
NGINX_SITE="${NGINX_SITE:-/etc/nginx/sites-available/live-auction}"
ACTION="${1:-status}"
export LIVE_AUCTION_ENV_FILE="${LIVE_AUCTION_ENV_FILE:-$SERVER_DIR/.env}"
export LIVE_AUCTION_RELEASE_ENV_FILE="${LIVE_AUCTION_RELEASE_ENV_FILE:-$SERVER_DIR/.release.env}"
RELEASE_IMAGE_VALIDATOR="${RELEASE_IMAGE_VALIDATOR:-$SERVER_DIR/backend/scripts/verify-prod-release-images.sh}"

compose() {
  docker compose --env-file "$LIVE_AUCTION_ENV_FILE" --env-file "$LIVE_AUCTION_RELEASE_ENV_FILE" -f "$BASE_COMPOSE" -f "$PERF_COMPOSE" "$@"
}

single_compose() {
  docker compose --env-file "$LIVE_AUCTION_ENV_FILE" --env-file "$LIVE_AUCTION_RELEASE_ENV_FILE" -f "$BASE_COMPOSE" "$@"
}

require_file() {
  if [[ ! -f "$1" ]]; then
    echo "Missing required file: $1" >&2
    exit 1
  fi
}

require_release_images() {
  require_file "$LIVE_AUCTION_ENV_FILE"
  require_file "$LIVE_AUCTION_RELEASE_ENV_FILE"
  require_file "$RELEASE_IMAGE_VALIDATOR"
  "$RELEASE_IMAGE_VALIDATOR" "$LIVE_AUCTION_RELEASE_ENV_FILE" --inspect
}

install_nginx() {
  local source="$1"
  require_file "$source"
  cp "$source" "$NGINX_SITE"
  ln -sf "$NGINX_SITE" /etc/nginx/sites-enabled/live-auction
  nginx -t
  systemctl reload nginx
}

wait_url() {
  local label="$1"
  local url="$2"
  local i
  for i in $(seq 1 30); do
    if curl -fsS "$url" >/dev/null; then
      echo "$label ready: $url"
      return 0
    fi
    sleep 2
  done
  echo "$label did not become ready: $url" >&2
  return 1
}

up() {
  require_file "$BASE_COMPOSE"
  require_file "$PERF_COMPOSE"
  require_file "$PERF_NGINX"
  compose up -d --no-build auction-backend auction-backend-2 auction-backend-3
  wait_url "backend-1" "http://127.0.0.1:18080/readyz"
  wait_url "backend-2" "http://127.0.0.1:18081/readyz"
  wait_url "backend-3" "http://127.0.0.1:18082/readyz"
  install_nginx "$PERF_NGINX"
  wait_url "unified entry" "http://127.0.0.1/readyz"
  compose ps auction-backend auction-backend-2 auction-backend-3
}

down() {
  require_file "$BASE_COMPOSE"
  if [[ -f "$SINGLE_NGINX" ]]; then
    install_nginx "$SINGLE_NGINX"
  else
    echo "Single-node nginx config not found at $SINGLE_NGINX; skipping nginx rollback" >&2
  fi
  compose stop auction-backend-2 auction-backend-3 || true
  compose rm -f auction-backend-2 auction-backend-3 || true
  single_compose up -d --no-build auction-backend
  wait_url "single backend" "http://127.0.0.1:18080/readyz"
  wait_url "single unified entry" "http://127.0.0.1/readyz"
  single_compose ps auction-backend
}

status() {
  if [[ -f "$PERF_COMPOSE" ]]; then
    compose ps auction-backend auction-backend-2 auction-backend-3
  else
    single_compose ps auction-backend
  fi
  for port in 18080 18081 18082; do
    curl -fsS -o /dev/null -w "backend ${port}: %{http_code}\n" "http://127.0.0.1:${port}/readyz" || true
  done
  curl -fsS -o /dev/null -w "nginx unified: %{http_code}\n" "http://127.0.0.1/readyz" || true
}

require_release_images

case "$ACTION" in
  up)
    up
    ;;
  down|rollback)
    down
    ;;
  status)
    status
    ;;
  *)
    echo "Usage: $0 {up|down|rollback|status}" >&2
    exit 2
    ;;
esac
