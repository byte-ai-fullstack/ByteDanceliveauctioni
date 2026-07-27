#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
validator="$repo_root/scripts/verify-prod-release-images.sh"
tmp_dir="$(mktemp -d)"
trap 'rm -rf -- "$tmp_dir"' EXIT

backend_id="sha256:$(printf '1%.0s' {1..64})"
elasticsearch_id="sha256:$(printf '2%.0s' {1..64})"
valid_manifest="$tmp_dir/release.env"
printf 'LIVE_AUCTION_BACKEND_IMAGE=%s\nLIVE_AUCTION_ELASTICSEARCH_IMAGE=%s\n' \
  "$backend_id" "$elasticsearch_id" >"$valid_manifest"

"$validator" "$valid_manifest" >/dev/null

docker() {
  local expected="${*: -1}"
  if [[ "${DOCKER_INSPECT_MISMATCH:-0}" == "1" && "$expected" == "$backend_id" ]]; then
    printf 'sha256:%s\n' "$(printf '3%.0s' {1..64})"
    return
  fi
  printf '%s\n' "$expected"
}
export -f docker
export backend_id

"$validator" "$valid_manifest" --inspect >/dev/null
if DOCKER_INSPECT_MISMATCH=1 "$validator" "$valid_manifest" --inspect >/dev/null 2>&1; then
  echo "validator accepted an image identity mismatch" >&2
  exit 1
fi
unset -f docker

assert_rejected() {
  local name="$1"
  local body="$2"
  local fixture="$tmp_dir/$name.env"
  printf '%s\n' "$body" >"$fixture"
  if "$validator" "$fixture" >/dev/null 2>&1; then
    echo "validator accepted invalid fixture: $name" >&2
    exit 1
  fi
}

assert_rejected tag-only $'LIVE_AUCTION_BACKEND_IMAGE=live-auction-bid-backend:prod\nLIVE_AUCTION_ELASTICSEARCH_IMAGE=live-auction-elasticsearch:prod'
assert_rejected zero-digest "LIVE_AUCTION_BACKEND_IMAGE=sha256:$(printf '0%.0s' {1..64})"$'\n'"LIVE_AUCTION_ELASTICSEARCH_IMAGE=$elasticsearch_id"
assert_rejected missing-search "LIVE_AUCTION_BACKEND_IMAGE=$backend_id"
assert_rejected duplicate $'LIVE_AUCTION_BACKEND_IMAGE=sha256:1111111111111111111111111111111111111111111111111111111111111111\nLIVE_AUCTION_BACKEND_IMAGE=sha256:3333333333333333333333333333333333333333333333333333333333333333\nLIVE_AUCTION_ELASTICSEARCH_IMAGE=sha256:2222222222222222222222222222222222222222222222222222222222222222'
assert_rejected extra-key "LIVE_AUCTION_BACKEND_IMAGE=$backend_id"$'\n'"LIVE_AUCTION_ELASTICSEARCH_IMAGE=$elasticsearch_id"$'\nRELEASE_CHANNEL=prod'

if rg -n 'image:\s+live-auction-(bid-backend|elasticsearch):prod' "$repo_root/deploy/prod" -g '*.yml'; then
  echo "production Compose still contains a mutable project image tag" >&2
  exit 1
fi

backend_declarations="$(rg -l 'image: "\$\{LIVE_AUCTION_BACKEND_IMAGE:\?LIVE_AUCTION_BACKEND_IMAGE is required\}"' "$repo_root/deploy/prod" -g '*.yml' | wc -l)"
if [[ "$backend_declarations" -ne 2 ]]; then
  echo "expected immutable backend image wiring in two production Compose files, got $backend_declarations" >&2
  exit 1
fi

search_declarations="$(rg -c 'image: "\$\{LIVE_AUCTION_ELASTICSEARCH_IMAGE:\?LIVE_AUCTION_ELASTICSEARCH_IMAGE is required\}"' "$repo_root/deploy/prod/docker-compose.yml")"
if [[ "$search_declarations" -ne 2 ]]; then
  echo "expected two immutable Elasticsearch image declarations, got $search_declarations" >&2
  exit 1
fi

rg -q 'docker build --file .*deploy/Dockerfile' "$repo_root/scripts/deploy-prod.sh"
rg -q 'docker build --file .*deploy/elasticsearch/Dockerfile' "$repo_root/scripts/deploy-prod.sh"
rg -q 'verify-prod-release-images\.sh .*--inspect' "$repo_root/scripts/deploy-prod.sh"
rg -q 'docker compose --env-file \.env --env-file \.release\.env up -d --no-build' "$repo_root/scripts/deploy-prod.sh"

runtime_env="$tmp_dir/runtime.env"
: >"$runtime_env"
while IFS= read -r variable; do
  printf '%s=test\n' "$variable" >>"$runtime_env"
done < <(rg -o '\$\{[A-Z0-9_]+:\?' \
  "$repo_root/deploy/prod/docker-compose.yml" \
  "$repo_root/deploy/prod/docker-compose.perf-3x.yml" \
  | sed -E 's/^.*\$\{([A-Z0-9_]+):\?$/\1/' | sort -u)
printf 'LIVE_AUCTION_ENV_FILE=%s\n' "$runtime_env" >>"$runtime_env"

compose_env=(--env-file "$runtime_env" --env-file "$valid_manifest")
docker compose "${compose_env[@]}" -f "$repo_root/deploy/prod/docker-compose.yml" config --quiet
docker compose "${compose_env[@]}" -f "$repo_root/deploy/prod/docker-compose.yml" -f "$repo_root/deploy/prod/docker-compose.perf-3x.yml" config --quiet

echo "Production Compose requires verified immutable backend and Elasticsearch image IDs."
