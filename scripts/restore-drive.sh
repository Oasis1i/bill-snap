#!/usr/bin/env bash
# Download latest/bills.db when the local database is missing or has no bills.
# --force replaces a database that already has rows. A previous non-empty file
# is copied to DB_PATH.before-restore before it is replaced.
# The download is checked, then moved into place. .env is never downloaded.
set -euo pipefail

SCRIPT_NAME=restore-drive
# shellcheck source=scripts/drive-env.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/drive-env.sh"

FORCE=0
case "${1:-}" in
  "") ;;
  --force) FORCE=1 ;;
  -h|--help)
    echo "usage: $SCRIPT_NAME.sh [--force]"
    exit 0
    ;;
  *)
    echo "usage: $SCRIPT_NAME.sh [--force]" >&2
    exit 2
    ;;
esac
if [[ $# -gt 1 ]]; then
  echo "usage: $SCRIPT_NAME.sh [--force]" >&2
  exit 2
fi

require_cmd sqlite3
require_cmd rclone
cd "$ROOT"

# Refuse when bill-snap or another process still has the SQLite files open.
# Overwriting a live database file can corrupt it or leave the process on the old pages.
_warned_no_holder_tool=0
refuse_if_open() {
  local -a targets=()
  local p raw pids
  for p in "$DB_PATH" "${DB_PATH}-wal" "${DB_PATH}-shm"; do
    if [[ -e "$p" ]]; then
      targets+=("$p")
    fi
  done
  if [[ ${#targets[@]} -eq 0 ]]; then
    return 0
  fi
  if command -v lsof >/dev/null 2>&1; then
    raw="$(lsof -t -- "${targets[@]}" 2>/dev/null || true)"
  elif command -v fuser >/dev/null 2>&1; then
    raw="$(fuser "${targets[@]}" 2>/dev/null || true)"
  else
    if [[ "$_warned_no_holder_tool" -eq 0 ]]; then
      echo "$SCRIPT_NAME: warning: lsof and fuser are unavailable, so this script cannot see whether a process holds $DB_PATH. Stop bill-snap and bill-snap-mcp before restoring." >&2
      _warned_no_holder_tool=1
    fi
    return 0
  fi
  pids="$(printf '%s\n' "$raw" | grep -oE '[0-9]+' | sort -u | tr '\n' ' ' | sed 's/ $//' || true)"
  if [[ -n "$pids" ]]; then
    echo "$SCRIPT_NAME: warning: $DB_PATH may be held by pid ${pids}. Stop bill-snap and bill-snap-mcp, then rerun." >&2
    exit 1
  fi
}

classify_local() {
  if [[ ! -e "$DB_PATH" ]]; then
    echo missing
    return 0
  fi
  if [[ ! -f "$DB_PATH" || ! -s "$DB_PATH" ]]; then
    echo empty
    return 0
  fi
  local magic tables rows
  magic="$(head -c 15 -- "$DB_PATH" || true)"
  if [[ "$magic" != "SQLite format 3" ]]; then
    echo unreadable
    return 0
  fi
  if ! sqlite3 -readonly -bail "$DB_PATH" "SELECT 1;" >/dev/null 2>&1; then
    echo unreadable
    return 0
  fi
  tables="$(sqlite3 -readonly -bail "$DB_PATH" "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='bills';")"
  if [[ "$tables" != "1" ]]; then
    echo empty
    return 0
  fi
  rows="$(sqlite3 -readonly -bail "$DB_PATH" "SELECT COUNT(*) FROM bills;")"
  if [[ "$rows" == "0" ]]; then
    echo empty
    return 0
  fi
  echo "nonempty:$rows"
}

refuse_if_open
state="$(classify_local)"
case "$state" in
  missing|empty) ;;
  unreadable)
    if [[ "$FORCE" -ne 1 ]]; then
      echo "$SCRIPT_NAME: $DB_PATH is not a readable bills database. Pass --force to replace it." >&2
      exit 1
    fi
    ;;
  nonempty:*)
    if [[ "$FORCE" -ne 1 ]]; then
      echo "$SCRIPT_NAME: $DB_PATH already has ${state#nonempty:} bills. Pass --force to replace it." >&2
      exit 1
    fi
    ;;
  *)
    echo "$SCRIPT_NAME: could not inspect $DB_PATH" >&2
    exit 1
    ;;
esac

parent="$(dirname "$DB_PATH")"
mkdir -p "$parent"
tmp="${parent}/.$(basename "$DB_PATH").restore.$$"
cleanup() {
  rm -f "$tmp" "${tmp}-wal" "${tmp}-shm"
}
trap cleanup EXIT

src="$(drive_object latest/bills.db)"
if ! dl_err="$(rclone copyto --ignore-times -q "$src" "$tmp" 2>&1)"; then
  echo "$SCRIPT_NAME: download failed: ${src}${dl_err:+: $dl_err}" >&2
  exit 1
fi
chmod 600 "$tmp"

magic="$(head -c 15 -- "$tmp" || true)"
if [[ "$magic" != "SQLite format 3" ]]; then
  echo "$SCRIPT_NAME: downloaded file is not a SQLite database: $src" >&2
  exit 1
fi
integrity="$(sqlite3 -bail -readonly "$tmp" "PRAGMA integrity_check;")"
if [[ "$integrity" != "ok" ]]; then
  echo "$SCRIPT_NAME: downloaded integrity_check failed: $integrity" >&2
  exit 1
fi
has_bills="$(sqlite3 -bail -readonly "$tmp" "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='bills';")"
if [[ "$has_bills" != "1" ]]; then
  echo "$SCRIPT_NAME: downloaded file has no bills table: $src" >&2
  exit 1
fi
bills="$(sqlite3 -bail -readonly "$tmp" "SELECT COUNT(*) FROM bills;")"
sqlite3 -bail "$tmp" >/dev/null <<EOF
PRAGMA busy_timeout=5000;
PRAGMA wal_checkpoint(TRUNCATE);
EOF
rm -f "${tmp}-wal" "${tmp}-shm"

refuse_if_open

saved=""
if [[ -f "$DB_PATH" && -s "$DB_PATH" ]]; then
  saved="${DB_PATH}.before-restore"
  cp -p -- "$DB_PATH" "$saved"
fi
mv -f -- "$tmp" "$DB_PATH"

noun=bills
[[ "$bills" -eq 1 ]] && noun=bill
if [[ -n "$saved" ]]; then
  echo "$SCRIPT_NAME: ok, ${bills} ${noun}, ${src} -> ${DB_PATH} (previous file saved to ${saved})"
else
  echo "$SCRIPT_NAME: ok, ${bills} ${noun}, ${src} -> ${DB_PATH}"
fi
