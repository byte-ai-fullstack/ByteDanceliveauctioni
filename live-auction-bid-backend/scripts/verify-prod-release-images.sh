#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: $0 RELEASE_ENV_FILE [--inspect]" >&2
}

if [[ $# -lt 1 || $# -gt 2 ]]; then
  usage
  exit 2
fi

release_env_file="$1"
inspect_images=0
if [[ $# -eq 2 ]]; then
  if [[ "$2" != "--inspect" ]]; then
    usage
    exit 2
  fi
  inspect_images=1
fi

if [[ ! -f "$release_env_file" ]]; then
  echo "Missing production release image manifest: $release_env_file" >&2
  exit 1
fi

backend_image=""
elasticsearch_image=""
line_number=0

while IFS= read -r line || [[ -n "$line" ]]; do
  line_number=$((line_number + 1))
  if [[ "$line" == *$'\r'* || ! "$line" =~ ^([A-Z0-9_]+)=(.+)$ ]]; then
    echo "$release_env_file:$line_number is not a KEY=value entry" >&2
    exit 1
  fi

  key="${BASH_REMATCH[1]}"
  value="${BASH_REMATCH[2]}"
  case "$key" in
    LIVE_AUCTION_BACKEND_IMAGE)
      if [[ -n "$backend_image" ]]; then
        echo "$release_env_file contains duplicate LIVE_AUCTION_BACKEND_IMAGE entries" >&2
        exit 1
      fi
      backend_image="$value"
      ;;
    LIVE_AUCTION_ELASTICSEARCH_IMAGE)
      if [[ -n "$elasticsearch_image" ]]; then
        echo "$release_env_file contains duplicate LIVE_AUCTION_ELASTICSEARCH_IMAGE entries" >&2
        exit 1
      fi
      elasticsearch_image="$value"
      ;;
    *)
      echo "$release_env_file contains unsupported key: $key" >&2
      exit 1
      ;;
  esac
done <"$release_env_file"

validate_image_id() {
  local key="$1"
  local value="$2"
  local digest

  if [[ ! "$value" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "$key must be an immutable lowercase Docker image ID" >&2
    exit 1
  fi
  digest="${value#sha256:}"
  if [[ "$digest" =~ ^0+$ ]]; then
    echo "$key must not use the all-zero release sentinel" >&2
    exit 1
  fi
}

validate_image_id LIVE_AUCTION_BACKEND_IMAGE "$backend_image"
validate_image_id LIVE_AUCTION_ELASTICSEARCH_IMAGE "$elasticsearch_image"

if [[ "$inspect_images" -eq 1 ]]; then
  for expected in "$backend_image" "$elasticsearch_image"; do
    actual="$(docker image inspect --format '{{.Id}}' "$expected")"
    if [[ "$actual" != "$expected" ]]; then
      echo "Loaded image identity mismatch: expected $expected, got $actual" >&2
      exit 1
    fi
  done
fi

echo "Production release image manifest verified."
