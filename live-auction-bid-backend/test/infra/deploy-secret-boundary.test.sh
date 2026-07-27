#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

rg -q '^deploy/\.env export-ignore$' "$repo_root/.gitattributes"
rg -q '^deploy/\.env$' "$repo_root/.dockerignore"
rg -q '^deploy/\.env$' "$repo_root/.gitignore"
if rg -q '^!deploy/\.env$' "$repo_root/.gitignore"; then
  echo "deploy/.env is still explicitly unignored" >&2
  exit 1
fi
if git -C "$repo_root" ls-files --error-unmatch deploy/.env >/dev/null 2>&1; then
  echo "deploy/.env must not be tracked by Git" >&2
  exit 1
fi
rg -q 'Missing pre-provisioned production environment' "$repo_root/scripts/deploy-prod.sh"
rg -q 'Refusing deployment archive containing deploy/\.env' "$repo_root/scripts/deploy-prod.sh"
rg -q '^  GITLEAKS_VERSION: 8\.30\.1$' "$repo_root/.github/workflows/ci.yml"
rg -q 'test -z .*git ls-files -- deploy/\.env' "$repo_root/.github/workflows/ci.yml"
rg -q 'git archive --format=tar HEAD' "$repo_root/.github/workflows/ci.yml"
rg -q 'test ! -e .*release/deploy/\.env' "$repo_root/.github/workflows/ci.yml"
rg -q 'github\.com/zricethezav/gitleaks/v8@v\$\{GITLEAKS_VERSION\}' "$repo_root/.github/workflows/ci.yml"
rg -q 'dir --redact=100 --no-banner --config .*release/\.gitleaks\.toml' "$repo_root/.github/workflows/ci.yml"
rg -Fq 'targetRules = ["curl-auth-user"]' "$repo_root/.gitleaks.toml"
rg -Fq 'condition = "AND"' "$repo_root/.gitleaks.toml"
rg -Fq "paths = ['''deploy/prod/docker-compose\.yml$''']" "$repo_root/.gitleaks.toml"

if rg -q 'cp backend\.new/deploy/\.env \.env' "$repo_root/scripts/deploy-prod.sh"; then
  echo "deploy-prod.sh still copies a repository environment file to production" >&2
  exit 1
fi
if rg -q 'intentionally tracks deploy/\.env' "$repo_root/deploy/.env.example"; then
  echo "deploy/.env.example still instructs operators to track secrets" >&2
  exit 1
fi

echo "deploy/.env is untracked and ignored; deployment artifacts exclude it, the tracked tree is secret-scanned, and production requires a pre-provisioned environment."
