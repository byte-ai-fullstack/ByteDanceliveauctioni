#!/usr/bin/env bash
set -euo pipefail

mysql_host="${MYSQL_HOST:?MYSQL_HOST is required}"
mysql_port="${MYSQL_PORT:-3306}"
mysql_user="${MYSQL_USER:?MYSQL_USER is required}"
mysql_database="${MYSQL_DATABASE:?MYSQL_DATABASE is required}"
password_file="${MYSQL_PASSWORD_FILE:?MYSQL_PASSWORD_FILE is required}"
backup_dir="${MYSQL_BACKUP_DIR:?MYSQL_BACKUP_DIR is required}"
mysqldump_bin="${MYSQLDUMP_BIN:-mysqldump}"

if ! [[ "$mysql_port" =~ ^[1-9][0-9]*$ ]] || ((mysql_port > 65535)); then
  echo "MYSQL_PORT must be a valid TCP port" >&2
  exit 2
fi
if ! [[ "$mysql_database" =~ ^[A-Za-z0-9_]+$ ]]; then
  echo "MYSQL_DATABASE contains unsafe characters" >&2
  exit 2
fi
if [[ ! -r "$password_file" ]]; then
  echo "MYSQL_PASSWORD_FILE must reference a readable file" >&2
  exit 2
fi

mkdir -p -- "$backup_dir"
resolved_backup_dir="$(cd "$backup_dir" && pwd -P)"
if [[ "$resolved_backup_dir" == "/" ]]; then
  echo "MYSQL_BACKUP_DIR cannot be the filesystem root" >&2
  exit 2
fi

umask 077
timestamp="${BACKUP_TIMESTAMP:-$(date -u +%Y%m%dT%H%M%SZ)}"
if ! [[ "$timestamp" =~ ^[0-9]{8}T[0-9]{6}Z$ ]]; then
  echo "BACKUP_TIMESTAMP must use YYYYMMDDTHHMMSSZ" >&2
  exit 2
fi
archive="$resolved_backup_dir/${mysql_database}-${timestamp}.sql.gz"
partial_archive="$archive.partial"
defaults_file="$(mktemp "$resolved_backup_dir/.mysql-backup.XXXXXX.cnf")"

cleanup() {
  rm -f -- "$defaults_file" "$partial_archive"
}
trap cleanup EXIT

if [[ -e "$archive" || -e "$archive.sha256" ]]; then
  echo "backup already exists for timestamp $timestamp" >&2
  exit 1
fi

password="$(<"$password_file")"
printf '[client]\nhost=%s\nport=%s\nuser=%s\npassword=%s\nprotocol=tcp\n' \
  "$mysql_host" "$mysql_port" "$mysql_user" "$password" >"$defaults_file"
unset password

"$mysqldump_bin" \
  --defaults-extra-file="$defaults_file" \
  --single-transaction \
  --set-gtid-purged=OFF \
  --routines \
  --events \
  --triggers \
  --hex-blob \
  "$mysql_database" |
  gzip -9 >"$partial_archive"

mv -- "$partial_archive" "$archive"
sha256sum "$archive" >"$archive.sha256"
printf '%s\n' "$archive"
