#!/usr/bin/env bash
# Upload a consistent SQLite snapshot to Google Drive with rclone.
# The live database stays local and is never uploaded mid-write.
# .env, tokens, and API keys are not uploaded. Run this from host cron.
#
# Pruning: after a successful upload, list backups/bills-YYYYMMDD-HHMM.db,
# sort by name (the timestamp makes that chronological), and delete the
# oldest until DRIVE_BACKUP_KEEP copies remain. DRIVE_BACKUP_KEEP=0 keeps
# every copy. On Google Drive, rclone deletefile uses the remote's trash
# when drive-use-trash is left at its default.
set -euo pipefail

SCRIPT_NAME=backup-drive
# shellcheck source=scripts/drive-env.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/drive-env.sh"

if [[ $# -ne 0 ]]; then
  echo "usage: $SCRIPT_NAME.sh" >&2
  exit 2
fi

require_cmd sqlite3
require_cmd rclone
cd "$ROOT"

if [[ ! -f "$DB_PATH" ]]; then
  echo "$SCRIPT_NAME: database not found: $DB_PATH" >&2
  exit 1
fi
if [[ ! -s "$DB_PATH" ]]; then
  echo "$SCRIPT_NAME: database is empty: $DB_PATH" >&2
  exit 1
fi
magic="$(head -c 15 -- "$DB_PATH" || true)"
if [[ "$magic" != "SQLite format 3" ]]; then
  echo "$SCRIPT_NAME: $DB_PATH is not a SQLite database" >&2
  exit 1
fi

stamp="$(TZ="$APP_TZ" date +%Y%m%d-%H%M)"
if [[ ! "$stamp" =~ ^[0-9]{8}-[0-9]{4}$ ]]; then
  echo "$SCRIPT_NAME: could not format a timestamp in $APP_TZ" >&2
  exit 1
fi

stage_dir="$(mktemp -d)"
chmod 700 "$stage_dir"
stage="$stage_dir/bills.db"
cleanup() {
  rm -rf "$stage_dir"
}
trap cleanup EXIT

# TRUNCATE folds the WAL into the main file when no other connection is
# mid-read. A running bill-snap can make that return busy. PASSIVE still
# checkpoints what it can, and .backup writes a consistent standalone file
# through SQLite's online backup API. Only that staging file is uploaded.
if ! ckpt_err="$(sqlite3 -bail "$DB_PATH" "PRAGMA busy_timeout=5000; PRAGMA wal_checkpoint(TRUNCATE);" 2>&1 >/dev/null)"; then
  echo "$SCRIPT_NAME: TRUNCATE checkpoint did not complete${ckpt_err:+: $ckpt_err}; using PASSIVE checkpoint and .backup" >&2
  sqlite3 "$DB_PATH" "PRAGMA busy_timeout=5000; PRAGMA wal_checkpoint(PASSIVE);" >/dev/null \
    || echo "$SCRIPT_NAME: PASSIVE checkpoint did not complete; continuing with .backup" >&2
fi
sqlite3 -bail "$DB_PATH" >/dev/null <<EOF
PRAGMA busy_timeout=5000;
.backup '$stage'
EOF
chmod 600 "$stage"

integrity="$(sqlite3 -bail -readonly "$stage" "PRAGMA integrity_check;")"
if [[ "$integrity" != "ok" ]]; then
  echo "$SCRIPT_NAME: snapshot integrity_check failed: $integrity" >&2
  exit 1
fi
has_bills="$(sqlite3 -bail -readonly "$stage" "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='bills';")"
if [[ "$has_bills" != "1" ]]; then
  echo "$SCRIPT_NAME: snapshot has no bills table; refusing to upload" >&2
  exit 1
fi
bills="$(sqlite3 -bail -readonly "$stage" "SELECT COUNT(*) FROM bills;")"
# .backup keeps the source journal mode. Fold any sidecar back into the
# snapshot so the uploaded file does not depend on a -wal that we never send.
sqlite3 -bail "$stage" >/dev/null <<EOF
PRAGMA busy_timeout=5000;
PRAGMA wal_checkpoint(TRUNCATE);
EOF
rm -f "${stage}-wal" "${stage}-shm"

latest="$(drive_object latest/bills.db)"
stamped="$(drive_object "backups/bills-${stamp}.db")"
rclone mkdir "$(drive_object latest)"
rclone mkdir "$(drive_object backups)"
# --ignore-times: size can stay the same when a row changes. Always send this snapshot.
rclone copyto --ignore-times -q "$stage" "$latest"
rclone copyto --ignore-times -q "$stage" "$stamped"

pruned=0
if [[ "$DRIVE_BACKUP_KEEP" -gt 0 ]]; then
  listing="$(rclone lsf "$(drive_object backups)" --files-only)"
  mapfile -t names < <(printf '%s\n' "$listing" | sed -n '/^bills-[0-9]\{8\}-[0-9]\{4\}\.db$/p' | LC_ALL=C sort)
  count="${#names[@]}"
  if [[ "$count" -gt "$DRIVE_BACKUP_KEEP" ]]; then
    remove=$((count - DRIVE_BACKUP_KEEP))
    for ((i = 0; i < remove; i++)); do
      rclone deletefile "$(drive_object "backups/${names[$i]}")" -q
    done
    pruned=$remove
  fi
fi

noun=bills
[[ "$bills" -eq 1 ]] && noun=bill
if [[ "$pruned" -gt 0 ]]; then
  echo "$SCRIPT_NAME: ok, ${bills} ${noun}, ${latest}, ${stamped}, pruned ${pruned}"
else
  echo "$SCRIPT_NAME: ok, ${bills} ${noun}, ${latest}, ${stamped}"
fi
