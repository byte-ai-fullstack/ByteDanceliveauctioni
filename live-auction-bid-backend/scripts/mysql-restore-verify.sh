#!/usr/bin/env bash
set -euo pipefail

mysql_host="${MYSQL_HOST:?MYSQL_HOST is required}"
mysql_port="${MYSQL_PORT:-3306}"
mysql_user="${MYSQL_USER:?MYSQL_USER is required}"
password_file="${MYSQL_PASSWORD_FILE:?MYSQL_PASSWORD_FILE is required}"
archive="${MYSQL_BACKUP_ARCHIVE:?MYSQL_BACKUP_ARCHIVE is required}"
restore_database="${MYSQL_RESTORE_DATABASE:?MYSQL_RESTORE_DATABASE is required}"
mysql_bin="${MYSQL_BIN:-mysql}"

if ! [[ "$mysql_port" =~ ^[1-9][0-9]*$ ]] || ((mysql_port > 65535)); then
  echo "MYSQL_PORT must be a valid TCP port" >&2
  exit 2
fi
if ! [[ "$restore_database" =~ ^restore_[A-Za-z0-9_]+$ ]]; then
  echo "MYSQL_RESTORE_DATABASE must start with restore_ and contain only letters, numbers and underscores" >&2
  exit 2
fi
if [[ "${CONFIRM_MYSQL_RESTORE:-}" != "$restore_database" ]]; then
  echo "set CONFIRM_MYSQL_RESTORE to the exact isolated restore database name" >&2
  exit 2
fi
if [[ ! -r "$password_file" || ! -r "$archive" || ! -r "$archive.sha256" ]]; then
  echo "password, backup archive and checksum files must all be readable" >&2
  exit 2
fi
sha256sum --check --status "$archive.sha256"

umask 077
defaults_file="$(mktemp /tmp/live-auction-mysql-restore.XXXXXX.cnf)"
trap 'rm -f -- "$defaults_file"' EXIT
password="$(<"$password_file")"
printf '[client]\nhost=%s\nport=%s\nuser=%s\npassword=%s\nprotocol=tcp\n' \
  "$mysql_host" "$mysql_port" "$mysql_user" "$password" >"$defaults_file"
unset password

database_exists="$("$mysql_bin" --defaults-extra-file="$defaults_file" --batch --skip-column-names \
  -e "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = '$restore_database'")"
if [[ "$database_exists" != "0" ]]; then
  echo "restore target $restore_database already exists; refusing to overwrite it" >&2
  exit 1
fi

"$mysql_bin" --defaults-extra-file="$defaults_file" \
  -e "CREATE DATABASE \`$restore_database\` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"
gzip -dc -- "$archive" | "$mysql_bin" --defaults-extra-file="$defaults_file" "$restore_database"

table_count="$("$mysql_bin" --defaults-extra-file="$defaults_file" --batch --skip-column-names \
  -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = '$restore_database'")"
migration_count="$("$mysql_bin" --defaults-extra-file="$defaults_file" --batch --skip-column-names \
  -e "SELECT COUNT(*) FROM \`$restore_database\`.auction_schema_migrations")"
if ! [[ "$table_count" =~ ^[0-9]+$ ]] || ((table_count < 29)); then
  echo "restore verification found only $table_count tables; expected at least 29 including schema history" >&2
  exit 1
fi
if ! [[ "$migration_count" =~ ^[1-9][0-9]*$ ]]; then
  echo "restore verification found no applied schema migration" >&2
  exit 1
fi

printf 'restore_database=%s tables=%s migrations=%s status=verified\n' \
  "$restore_database" "$table_count" "$migration_count"
echo "The isolated restore database is intentionally retained for application-level verification." >&2
