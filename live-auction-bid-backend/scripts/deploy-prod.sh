#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKSPACE_DIR="$(cd "$ROOT_DIR/.." && pwd)"
BACKEND_DIR="${BACKEND_DIR:-$ROOT_DIR}"
ADMIN_DIR="${ADMIN_DIR:-$WORKSPACE_DIR/live-auction-bid-frontend}"
H5_DIR="${H5_DIR:-$WORKSPACE_DIR/live-auction-user-h5}"

SERVER_HOST="${SERVER_HOST:-120.79.7.110}"
SERVER_USER="${SERVER_USER:-root}"
SERVER_DIR="${SERVER_DIR:-/opt/live-auction}"
SSH_KEY_SOURCE="${SSH_KEY:-/home/ye/OpenClaw/workspace/plans/yexieer.pem}"
SSH_KEY_RUNTIME="${SSH_KEY_RUNTIME:-/tmp/live-auction-yexieer.pem}"
WORK_DIR="${WORK_DIR:-/tmp/live-auction-deploy-fast}"
INSTALL_NGINX_CONF="${INSTALL_NGINX_CONF:-0}"
INCLUDE_OBSERVABILITY_IMAGES="${INCLUDE_OBSERVABILITY_IMAGES:-1}"
BACKEND_BUILD_IMAGE="${BACKEND_BUILD_IMAGE:-live-auction-bid-backend:release-build}"
ELASTICSEARCH_BUILD_IMAGE="${ELASTICSEARCH_BUILD_IMAGE:-live-auction-elasticsearch:release-build}"

RUN_BACKEND=1
RUN_ADMIN=1
RUN_H5=1
RUN_TESTS=1

usage() {
  cat <<'EOF'
Usage: scripts/deploy-prod.sh [options]

Options:
  --frontend-only   Only build/upload admin + H5 static files.
  --backend-only    Only test/build/upload backend image.
  --skip-tests      Skip go test before backend image build.
  -h, --help        Show this help.

Environment:
  SSH_KEY=/path/to/key.pem
  SERVER_HOST=120.79.7.110
  SERVER_USER=root
  SERVER_DIR=/opt/live-auction
  INSTALL_NGINX_CONF=1
  INCLUDE_OBSERVABILITY_IMAGES=1

The deployment host must already contain SERVER_DIR/.env with mode 0600.
Application containers are started from image IDs recorded in SERVER_DIR/.release.env.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --frontend-only)
      RUN_BACKEND=0
      RUN_ADMIN=1
      RUN_H5=1
      ;;
    --backend-only)
      RUN_BACKEND=1
      RUN_ADMIN=0
      RUN_H5=0
      ;;
    --skip-tests)
      RUN_TESTS=0
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

require_file() {
  if [[ ! -f "$1" ]]; then
    echo "Missing required file: $1" >&2
    exit 1
  fi
}

run() {
  echo
  echo "==> $*"
  "$@"
}

save_images_archive() {
  local destination="$1"
  shift
  docker save "$@" | gzip -1 >"$destination"
}

build_frontend() {
  local app_dir="$1"
  local node_bin="${NODE_BIN:-node}"
  (
    cd "$app_dir"
    run "$node_bin" node_modules/typescript/bin/tsc -b
    run "$node_bin" node_modules/vite/bin/vite.js build
  )
}

ensure_clean_git() {
  local repo_dir="$1"
  local name="$2"
  if [[ -n "$(git -C "$repo_dir" status --porcelain)" ]]; then
    echo "$name has uncommitted changes. Commit or stash them before deploy." >&2
    git -C "$repo_dir" status --short >&2
    exit 1
  fi
}

prepare_ssh_key() {
  require_file "$SSH_KEY_SOURCE"
  cp "$SSH_KEY_SOURCE" "$SSH_KEY_RUNTIME"
  chmod 600 "$SSH_KEY_RUNTIME"
}

remote() {
  local attempt
  local ssh_opts
  mapfile -t ssh_opts < <(ssh_common_options)
  for attempt in 1 2 3; do
    if ssh "${ssh_opts[@]}" "$SERVER_USER@$SERVER_HOST" "$@"; then
      return 0
    fi
    sleep 5
  done
  return 1
}

upload() {
  local attempt
  local ssh_opts
  mapfile -t ssh_opts < <(ssh_common_options)
  for attempt in 1 2 3; do
    if scp "${ssh_opts[@]}" "$@" "$SERVER_USER@$SERVER_HOST:$SERVER_DIR/uploads/"; then
      return 0
    fi
    sleep 5
  done
  return 1
}

ssh_common_options() {
  printf '%s\n' \
    -i "$SSH_KEY_RUNTIME" \
    -o IdentitiesOnly=yes \
    -o BatchMode=yes \
    -o ConnectTimeout=15 \
    -o ServerAliveInterval=5 \
    -o ServerAliveCountMax=2 \
    -o ControlMaster=auto \
    -o ControlPersist=10m \
    -o ControlPath="$WORK_DIR/ssh-control-%r@%h:%p"
}

prepare_packages() {
  rm -rf "$WORK_DIR"
  mkdir -p "$WORK_DIR"

  if [[ "$RUN_BACKEND" -eq 1 ]]; then
    ensure_clean_git "$BACKEND_DIR" "backend"
    if [[ "$RUN_TESTS" -eq 1 ]]; then
      run env GOCACHE=/tmp/live-auction-go-cache go -C "$BACKEND_DIR" test ./...
    fi
    run docker build --file "$BACKEND_DIR/deploy/Dockerfile" --tag "$BACKEND_BUILD_IMAGE" "$BACKEND_DIR"
    run docker build --file "$BACKEND_DIR/deploy/elasticsearch/Dockerfile" --tag "$ELASTICSEARCH_BUILD_IMAGE" "$BACKEND_DIR"
    local backend_image_id
    local elasticsearch_image_id
    backend_image_id="$(docker image inspect --format '{{.Id}}' "$BACKEND_BUILD_IMAGE")"
    elasticsearch_image_id="$(docker image inspect --format '{{.Id}}' "$ELASTICSEARCH_BUILD_IMAGE")"
    printf 'LIVE_AUCTION_BACKEND_IMAGE=%s\nLIVE_AUCTION_ELASTICSEARCH_IMAGE=%s\n' \
      "$backend_image_id" "$elasticsearch_image_id" >"$WORK_DIR/release-images.env"
    run "$BACKEND_DIR/scripts/verify-prod-release-images.sh" "$WORK_DIR/release-images.env"
    run git -C "$BACKEND_DIR" archive --format=tar.gz -o "$WORK_DIR/backend-src.tar.gz" HEAD
    run save_images_archive "$WORK_DIR/application-images.tar.gz" "$backend_image_id" "$elasticsearch_image_id"
    if [[ "$INCLUDE_OBSERVABILITY_IMAGES" == "1" ]]; then
      local observability_images=(
        prom/prometheus:v2.53.1
        grafana/grafana:11.1.4
        danielqsj/kafka-exporter:v1.9.0
        oliver006/redis_exporter:v1.84.0
        natsio/prometheus-nats-exporter:0.18.0
        prometheuscommunity/elasticsearch-exporter:v1.10.0
        prometheuscommunity/postgres-exporter:v0.19.1
        quay.io/prometheus/mysqld-exporter:v0.19.0
        quay.io/prometheus/node-exporter:v1.11.1
      )
      local image
      for image in "${observability_images[@]}"; do
        run docker pull "$image"
      done
      run save_images_archive "$WORK_DIR/observability-images.tar.gz" "${observability_images[@]}"
    fi
  fi

  if [[ "$RUN_ADMIN" -eq 1 ]]; then
    ensure_clean_git "$ADMIN_DIR" "admin frontend"
    build_frontend "$ADMIN_DIR"
    run tar -czf "$WORK_DIR/admin-dist.tar.gz" -C "$ADMIN_DIR/dist" .
  fi

  if [[ "$RUN_H5" -eq 1 ]]; then
    ensure_clean_git "$H5_DIR" "H5 frontend"
    build_frontend "$H5_DIR"
    run tar -czf "$WORK_DIR/h5-dist.tar.gz" -C "$H5_DIR/dist" .
  fi
}

deploy_remote() {
  run remote "mkdir -p '$SERVER_DIR/uploads' '$SERVER_DIR/www' '$SERVER_DIR/backend'"

  local upload_files=()
  [[ "$RUN_BACKEND" -eq 1 ]] && upload_files+=("$WORK_DIR/backend-src.tar.gz" "$WORK_DIR/application-images.tar.gz" "$WORK_DIR/release-images.env")
  [[ "$RUN_BACKEND" -eq 1 && "$INCLUDE_OBSERVABILITY_IMAGES" == "1" ]] && upload_files+=("$WORK_DIR/observability-images.tar.gz")
  [[ "$RUN_ADMIN" -eq 1 ]] && upload_files+=("$WORK_DIR/admin-dist.tar.gz")
  [[ "$RUN_H5" -eq 1 ]] && upload_files+=("$WORK_DIR/h5-dist.tar.gz")

  run upload "${upload_files[@]}"

  local remote_script
  remote_script=$(cat <<EOF
set -euo pipefail
cd '$SERVER_DIR'

if [[ ! -f .env ]]; then
  echo "Missing pre-provisioned production environment: $SERVER_DIR/.env" >&2
  exit 1
fi
chmod 600 .env

if [[ $RUN_BACKEND -eq 1 ]]; then
  rm -rf backend.new
  mkdir -p backend.new
  tar -xzf uploads/backend-src.tar.gz -C backend.new
  if [[ -e backend.new/deploy/.env ]]; then
    echo "Refusing deployment archive containing deploy/.env" >&2
    exit 1
  fi
  install -m 0644 uploads/release-images.env .release.env.new
  backend.new/scripts/verify-prod-release-images.sh .release.env.new
  gzip -dc uploads/application-images.tar.gz | docker load
  backend.new/scripts/verify-prod-release-images.sh .release.env.new --inspect
  if [[ '$INCLUDE_OBSERVABILITY_IMAGES' == '1' && -f uploads/observability-images.tar.gz ]]; then
    gzip -dc uploads/observability-images.tar.gz | docker load
  fi
  cp backend.new/deploy/prod/docker-compose.yml docker-compose.yml
  cp backend.new/deploy/prod/live-auction.nginx.conf live-auction.nginx.conf
  if [[ '$INSTALL_NGINX_CONF' == '1' ]]; then
    cp live-auction.nginx.conf /etc/nginx/sites-available/live-auction
    ln -sf /etc/nginx/sites-available/live-auction /etc/nginx/sites-enabled/live-auction
  fi
  rm -rf backend
  mv backend.new backend
  mv .release.env.new .release.env
fi

if [[ ! -f .release.env ]]; then
  echo "Missing immutable production image manifest: $SERVER_DIR/.release.env" >&2
  exit 1
fi
backend/scripts/verify-prod-release-images.sh .release.env --inspect

if [[ $RUN_ADMIN -eq 1 ]]; then
  rm -rf www/admin.new
  mkdir -p www/admin.new
  tar -xzf uploads/admin-dist.tar.gz -C www/admin.new
  rm -rf www/admin
  mv www/admin.new www/admin
fi

if [[ $RUN_H5 -eq 1 ]]; then
  rm -rf www/h5.new
  mkdir -p www/h5.new
  tar -xzf uploads/h5-dist.tar.gz -C www/h5.new
  rm -rf www/h5
  mv www/h5.new www/h5
fi

chmod 755 '$SERVER_DIR' '$SERVER_DIR/www' '$SERVER_DIR/www/admin' '$SERVER_DIR/www/h5'
docker compose --env-file .env --env-file .release.env up -d --no-build
systemctl reload nginx

for i in \$(seq 1 30); do
  if curl -fsS http://127.0.0.1/readyz; then
    break
  fi
  if [[ "\$i" -eq 30 ]]; then
    echo "backend did not become ready" >&2
    exit 1
  fi
  sleep 2
done
curl -fsS -o /tmp/live-auction-h5.html -w "\\nH5 %{http_code} %{size_download}\\n" http://127.0.0.1/
curl -fsS -o /tmp/live-auction-admin.html -w "ADMIN %{http_code} %{size_download}\\n" http://127.0.0.1:8080/
docker compose --env-file .env --env-file .release.env ps
EOF
)

  run remote "bash -s" <<<"$remote_script"
}

prepare_ssh_key
prepare_packages
deploy_remote

echo
echo "Deploy complete:"
echo "  H5:    http://$SERVER_HOST/"
echo "  Admin: http://$SERVER_HOST:8080/"
