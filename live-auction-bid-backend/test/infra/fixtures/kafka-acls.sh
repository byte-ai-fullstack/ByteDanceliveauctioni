#!/usr/bin/env bash
set -euo pipefail

printf 'acls %s\n' "$*" >>"$KAFKA_FAKE_LOG"
