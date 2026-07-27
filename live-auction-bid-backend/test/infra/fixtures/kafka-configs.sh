#!/usr/bin/env bash
set -euo pipefail

printf 'configs %s\n' "$*" >>"$KAFKA_FAKE_LOG"
