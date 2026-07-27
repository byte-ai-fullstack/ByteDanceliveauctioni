#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mode="${1:-core}"
release_image="${AUCTION_RELEASE_IMAGE:-}"
go_binary="${GO_BINARY:-go}"
kustomize_version="${KUSTOMIZE_VERSION:-v5.7.1}"
zero_digest="sha256:0000000000000000000000000000000000000000000000000000000000000000"

case "$mode" in
  core) source_resource="../base" ;;
  search) source_resource="../overlays/search" ;;
  *)
    echo "Usage: AUCTION_RELEASE_IMAGE=repository:version@sha256:<64-hex> $0 [core|search]" >&2
    exit 2
    ;;
esac

if [[ ! "$release_image" =~ ^[a-z0-9][a-z0-9._:/-]*:[a-z0-9][a-z0-9._-]*@sha256:[0-9a-f]{64}$ ]]; then
  echo "AUCTION_RELEASE_IMAGE must include a lowercase immutable version tag and sha256 digest" >&2
  exit 2
fi
tagged_repository="${release_image%@sha256:*}"
repository="${tagged_repository%:*}"
tag="${tagged_repository##*:}"
digest="sha256:${release_image##*@sha256:}"
if [[ "$digest" == "$zero_digest" ]]; then
  echo "AUCTION_RELEASE_IMAGE cannot use the source-template zero digest" >&2
  exit 2
fi

render_dir="$(mktemp -d "$repo_root/deploy/kubernetes/.release-render.XXXXXX")"
trap 'rm -rf -- "$render_dir"' EXIT
printf '%s\n' \
  'apiVersion: kustomize.config.k8s.io/v1beta1' \
  'kind: Kustomization' \
  'resources:' \
  "  - $source_resource" \
  'images:' \
  '  - name: live-auction-bid-backend' \
  "    newName: $repository" \
  "    newTag: $tag" \
  "    digest: $digest" >"$render_dir/kustomization.yaml"

GOWORK=off "$go_binary" run "sigs.k8s.io/kustomize/kustomize/v5@${kustomize_version}" build \
  --load-restrictor LoadRestrictionsNone "$render_dir" >"$render_dir/rendered.yaml"

expected_count=8
if [[ "$mode" == "search" ]]; then
  expected_count=11
fi
actual_count="$(rg -c --fixed-strings "image: $release_image" "$render_dir/rendered.yaml")"
if [[ "$actual_count" != "$expected_count" ]]; then
  echo "Rendered $mode manifest contains $actual_count pinned application images, want $expected_count" >&2
  exit 1
fi
if rg -n -- 'image: .*:release([[:space:]]|$)|image: .*@sha256:0{64}([[:space:]]|$)' "$render_dir/rendered.yaml"; then
  echo "Rendered manifest still contains a mutable tag or zero-digest sentinel" >&2
  exit 1
fi
while IFS= read -r image_line; do
  image_ref="${image_line#*image: }"
  if [[ ! "$image_ref" =~ ^[a-z0-9][a-z0-9._:/-]*:[a-z0-9][a-z0-9._-]*@sha256:[0-9a-f]{64}$ ]]; then
    echo "Rendered manifest contains an image that is not pinned by sha256 digest: $image_ref" >&2
    exit 1
  fi
done < <(rg --no-line-number -- '^[[:space:]]*image:[[:space:]]+' "$render_dir/rendered.yaml")

sed -n '1,$p' "$render_dir/rendered.yaml"
