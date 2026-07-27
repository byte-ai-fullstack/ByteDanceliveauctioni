#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
lock_file="$repo_root/deploy/images.lock"
failure=0

if [[ ! -s "$lock_file" ]]; then
  echo "deploy/images.lock is missing or empty" >&2
  exit 1
fi

while IFS= read -r reference; do
  [[ -z "$reference" || "$reference" == \#* ]] && continue
  if ! [[ "$reference" =~ ^[^[:space:]]+:[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]]; then
    echo "invalid immutable image reference in deploy/images.lock: $reference" >&2
    failure=1
  fi
done <"$lock_file"

while IFS= read -r declaration; do
  file="${declaration%%:*}"
  remainder="${declaration#*:}"
  line="${remainder%%:*}"
  reference="${remainder#*:}"
  reference="${reference#*image:}"
  reference="${reference# }"

  # Production Compose receives verified local image IDs from .release.env.
  if [[ "$file" == "$repo_root/deploy/prod/"*.yml && \
        ( "$reference" == '"${LIVE_AUCTION_BACKEND_IMAGE:?LIVE_AUCTION_BACKEND_IMAGE is required}"' || \
          "$reference" == '"${LIVE_AUCTION_ELASTICSEARCH_IMAGE:?LIVE_AUCTION_ELASTICSEARCH_IMAGE is required}"' ) ]]; then
    continue
  fi

  # Development builds project images locally.
  if [[ "$file" == "$repo_root/deploy/docker-compose.yml" && \
        ( "$reference" == "live-auction-bid-backend:local" || "$reference" == "live-auction-elasticsearch:8.19.17-ik" ) ]]; then
    continue
  fi
  # Raw Kubernetes templates use a fail-closed sentinel; the release renderer
  # replaces it and check-kubernetes-manifests.sh rejects any unresolved sentinel.
  if [[ "$file" == "$repo_root/deploy/kubernetes/"* && \
        "$reference" =~ ^live-auction-bid-backend:release-template@sha256:0{64}$ ]]; then
    continue
  fi

  if [[ "$reference" == live-auction-bid-backend:* || "$reference" == live-auction-elasticsearch:* ]]; then
    if [[ ! "$reference" =~ ^[^[:space:]]+:[^[:space:]@]+@sha256:[0-9a-f]{64}$ || "$reference" =~ @sha256:0{64}$ ]]; then
      echo "$file:$line uses an unapproved project image reference: $reference" >&2
      failure=1
    fi
    continue
  fi
  if ! [[ "$reference" =~ ^[^[:space:]]+:[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]]; then
    echo "$file:$line uses a mutable image reference: $reference" >&2
    failure=1
    continue
  fi
  if ! grep -Fqx -- "$reference" "$lock_file"; then
    echo "$file:$line is not recorded in deploy/images.lock: $reference" >&2
    failure=1
  fi
done < <(rg -n --no-heading '^\s*image:\s*\S+' "$repo_root/deploy" "$repo_root/.github/workflows" -g '*.yml' -g '*.yaml')

while IFS= read -r declaration; do
  file="${declaration%%:*}"
  remainder="${declaration#*:}"
  line="${remainder%%:*}"
  reference="${remainder#*:}"
  reference="${reference#*FROM }"
  reference="${reference%% *}"

  if ! [[ "$reference" =~ ^[^[:space:]]+:[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]]; then
    echo "$file:$line uses a mutable build image reference: $reference" >&2
    failure=1
    continue
  fi
  if ! grep -Fqx -- "$reference" "$lock_file"; then
    echo "$file:$line build image is not recorded in deploy/images.lock: $reference" >&2
    failure=1
  fi
done < <(rg -n --no-heading '^FROM\s+\S+' "$repo_root" -g 'Dockerfile' -g 'Dockerfile.*')

if [[ "$failure" -ne 0 ]]; then
  exit 1
fi
