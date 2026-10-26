#!/usr/bin/env bash
# Shared settings for scripts/backup-drive.sh and scripts/restore-drive.sh.
# Only the keys listed below are read from .env. Tokens and API keys are ignored.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  echo "drive-env.sh is sourced by backup-drive.sh and restore-drive.sh" >&2
  exit 1
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT_NAME="${SCRIPT_NAME:-drive-backup}"

# First occurrence wins, and a variable already set in the environment wins.
# That matches internal/config load order for the keys we share (DB_PATH, APP_TZ).
_drive_load_dotenv() {
  local file="$ROOT/.env"
  [[ -f "$file" ]] || return 0
  local line key val allowed k
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    line="${line#"${line%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    [[ -z "$line" || "$line" == \#* ]] && continue
    if [[ "$line" == export[[:space:]]* ]]; then
      line="${line#export}"
      line="${line#"${line%%[![:space:]]*}"}"
    fi
    [[ "$line" == *=* ]] || continue
    key="${line%%=*}"
    val="${line#*=}"
    key="${key%"${key##*[![:space:]]}"}"
    val="${val#"${val%%[![:space:]]*}"}"
    val="${val%"${val##*[![:space:]]}"}"
    [[ -n "$key" ]] || continue
    if [[ ${#val} -ge 2 ]]; then
      if [[ ( "$val" == \"*\" && "$val" == *\" ) || ( "$val" == \'*\' && "$val" == *\' ) ]]; then
        val="${val:1:${#val}-2}"
      fi
    fi
    allowed=0
    for k in DB_PATH RCLONE_REMOTE DRIVE_BACKUP_ROOT APP_TZ DRIVE_BACKUP_KEEP; do
      if [[ "$k" == "$key" ]]; then
        allowed=1
        break
      fi
    done
    [[ "$allowed" -eq 1 ]] || continue
    if [[ -z "${!key+x}" ]]; then
      printf -v "$key" '%s' "$val"
      export "${key?}"
    fi
  done < "$file"
}

_drive_load_dotenv

DB_PATH="${DB_PATH:-data/bills.db}"
RCLONE_REMOTE="${RCLONE_REMOTE:-gdrive}"
DRIVE_BACKUP_ROOT="${DRIVE_BACKUP_ROOT:-bill-snap}"
APP_TZ="${APP_TZ:-Asia/Shanghai}"
DRIVE_BACKUP_KEEP="${DRIVE_BACKUP_KEEP:-14}"

DB_PATH="${DB_PATH#"${DB_PATH%%[![:space:]]*}"}"
DB_PATH="${DB_PATH%"${DB_PATH##*[![:space:]]}"}"
RCLONE_REMOTE="${RCLONE_REMOTE#"${RCLONE_REMOTE%%[![:space:]]*}"}"
RCLONE_REMOTE="${RCLONE_REMOTE%"${RCLONE_REMOTE##*[![:space:]]}"}"
DRIVE_BACKUP_ROOT="${DRIVE_BACKUP_ROOT#"${DRIVE_BACKUP_ROOT%%[![:space:]]*}"}"
DRIVE_BACKUP_ROOT="${DRIVE_BACKUP_ROOT%"${DRIVE_BACKUP_ROOT##*[![:space:]]}"}"
APP_TZ="${APP_TZ#"${APP_TZ%%[![:space:]]*}"}"
APP_TZ="${APP_TZ%"${APP_TZ##*[![:space:]]}"}"
DRIVE_BACKUP_KEEP="${DRIVE_BACKUP_KEEP#"${DRIVE_BACKUP_KEEP%%[![:space:]]*}"}"
DRIVE_BACKUP_KEEP="${DRIVE_BACKUP_KEEP%"${DRIVE_BACKUP_KEEP##*[![:space:]]}"}"

while [[ "$DRIVE_BACKUP_ROOT" == */ ]]; do
  DRIVE_BACKUP_ROOT="${DRIVE_BACKUP_ROOT%/}"
done

if [[ "$DB_PATH" != /* ]]; then
  DB_PATH="$ROOT/$DB_PATH"
fi

if [[ ! "$RCLONE_REMOTE" =~ ^[A-Za-z][A-Za-z0-9_-]{0,63}$ ]]; then
  echo "$SCRIPT_NAME: invalid RCLONE_REMOTE (use the rclone remote name, e.g. gdrive)" >&2
  exit 1
fi
if [[ -z "$DRIVE_BACKUP_ROOT" || "$DRIVE_BACKUP_ROOT" == /* || "$DRIVE_BACKUP_ROOT" == -* || "$DRIVE_BACKUP_ROOT" == *..* || "$DRIVE_BACKUP_ROOT" == *:* || ! "$DRIVE_BACKUP_ROOT" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]]; then
  echo "$SCRIPT_NAME: invalid DRIVE_BACKUP_ROOT: ${DRIVE_BACKUP_ROOT:-<empty>}" >&2
  exit 1
fi
if [[ -z "$APP_TZ" || "$APP_TZ" == *..* || "$APP_TZ" == /* || ! -e "/usr/share/zoneinfo/$APP_TZ" ]]; then
  echo "$SCRIPT_NAME: unknown APP_TZ: ${APP_TZ:-<empty>}" >&2
  exit 1
fi
if [[ -z "$DRIVE_BACKUP_KEEP" || ! "$DRIVE_BACKUP_KEEP" =~ ^[0-9]+$ ]]; then
  echo "$SCRIPT_NAME: DRIVE_BACKUP_KEEP must be a non-negative integer (0 keeps every copy)" >&2
  exit 1
fi
DRIVE_BACKUP_KEEP=$((10#$DRIVE_BACKUP_KEEP))

_resolved_db="$DB_PATH"
if [[ -e "$DB_PATH" ]]; then
  _resolved_db="$(readlink -f "$DB_PATH")"
fi
_resolved_env="$(readlink -f "$ROOT/.env" 2>/dev/null || printf '%s' "$ROOT/.env")"
case "$(basename "$_resolved_db")" in
  .env|.env.*)
    echo "$SCRIPT_NAME: refusing to use .env as DB_PATH" >&2
    exit 1
    ;;
esac
if [[ "$_resolved_db" == "$_resolved_env" ]]; then
  echo "$SCRIPT_NAME: refusing to use .env as DB_PATH" >&2
  exit 1
fi
unset _resolved_db _resolved_env

# rclone destination for a path under DRIVE_BACKUP_ROOT, e.g. latest/bills.db.
drive_object() {
  printf '%s:%s/%s' "$RCLONE_REMOTE" "$DRIVE_BACKUP_ROOT" "$1"
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "$SCRIPT_NAME: missing required command: $1" >&2
    exit 1
  fi
}
