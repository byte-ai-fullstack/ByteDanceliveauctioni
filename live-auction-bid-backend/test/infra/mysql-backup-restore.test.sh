#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
fixture_bin="$repo_root/test/infra/fixtures"
scratch_dir="$(mktemp -d)"
trap 'rm -rf -- "$scratch_dir"' EXIT
printf '%s' 'test-only-password' >"$scratch_dir/password"

archive="$({
  MYSQL_HOST=mysql.test \
    MYSQL_USER=backup \
    MYSQL_DATABASE=live_auction \
    MYSQL_PASSWORD_FILE="$scratch_dir/password" \
    MYSQL_BACKUP_DIR="$scratch_dir/backups" \
    MYSQLDUMP_BIN="$fixture_bin/mysqldump" \
    MYSQL_FAKE_LOG="$scratch_dir/mysql.log" \
    BACKUP_TIMESTAMP=20260726T120000Z \
    "$repo_root/scripts/mysql-backup.sh"
})"

test -s "$archive"
test -s "$archive.sha256"
gzip -dc "$archive" | rg -q 'CREATE TABLE restored_fixture'
rg -q -- '--single-transaction' "$scratch_dir/mysql.log"

if MYSQL_HOST=mysql.test MYSQL_USER=backup MYSQL_DATABASE=live_auction \
  MYSQL_PASSWORD_FILE="$scratch_dir/password" MYSQL_BACKUP_DIR="$scratch_dir/backups" \
  MYSQLDUMP_BIN="$fixture_bin/mysqldump" MYSQL_FAKE_LOG="$scratch_dir/mysql.log" \
  BACKUP_TIMESTAMP=20260726T120000Z "$repo_root/scripts/mysql-backup.sh" >/dev/null 2>&1; then
  echo "backup must not overwrite an existing archive" >&2
  exit 1
fi

restore_output="$({
  MYSQL_HOST=mysql.test \
    MYSQL_USER=restore \
    MYSQL_PASSWORD_FILE="$scratch_dir/password" \
    MYSQL_BACKUP_ARCHIVE="$archive" \
    MYSQL_RESTORE_DATABASE=restore_live_auction_test \
    CONFIRM_MYSQL_RESTORE=restore_live_auction_test \
    MYSQL_BIN="$fixture_bin/mysql" \
    MYSQL_FAKE_LOG="$scratch_dir/mysql.log" \
    MYSQL_FAKE_RESTORE_SQL="$scratch_dir/restored.sql" \
    "$repo_root/scripts/mysql-restore-verify.sh" 2>/dev/null
})"
[[ "$restore_output" == *"status=verified"* ]]
rg -q 'CREATE TABLE restored_fixture' "$scratch_dir/restored.sql"

if MYSQL_HOST=mysql.test MYSQL_USER=restore MYSQL_PASSWORD_FILE="$scratch_dir/password" \
  MYSQL_BACKUP_ARCHIVE="$archive" MYSQL_RESTORE_DATABASE=production \
  CONFIRM_MYSQL_RESTORE=production MYSQL_BIN="$fixture_bin/mysql" \
  "$repo_root/scripts/mysql-restore-verify.sh" >/dev/null 2>&1; then
  echo "restore must reject a non-isolated database name" >&2
  exit 1
fi
