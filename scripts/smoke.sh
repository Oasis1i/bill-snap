#!/usr/bin/env bash
# Curl the local server with the mock extractor, exercise POST /mcp,
# then ask the stdio MCP process to summarize the same SQLite file.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TMP="$(mktemp -d)"
PID=""
cleanup() {
  status=$?
  if [[ -n "${PID}" ]] && kill -0 "$PID" 2>/dev/null; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  if [[ "$status" -ne 0 && -f "$TMP/server.log" ]]; then
    echo "----- server log -----" >&2
    cat "$TMP/server.log" >&2
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

ADDR="127.0.0.1:18765"
export HTTP_ADDR="$ADDR"
export UPLOAD_TOKEN="smoke-token"
export MCP_TOKEN="smoke-mcp-token"
export DB_PATH="$TMP/bills.db"
export UPLOAD_DIR="$TMP/uploads"
export VISION_PROVIDER="mock"
export APP_TZ="Asia/Shanghai"
export MAX_UPLOAD_BYTES="8388608"

echo "building..."
go build -o "$TMP/bill-snap" ./cmd/server
go build -o "$TMP/bill-snap-mcp" ./cmd/mcp-server
go run scripts/genpng.go "$TMP/bill.png" "$TMP/blank.png"
printf 'not an image\n' > "$TMP/note.txt"

echo "starting server..."
"$TMP/bill-snap" >"$TMP/server.log" 2>&1 &
PID=$!

ready=0
for _ in $(seq 1 50); do
  if curl -sf "http://$ADDR/healthz" >/dev/null; then
    ready=1
    break
  fi
  if ! kill -0 "$PID" 2>/dev/null; then
    echo "server exited early" >&2
    cat "$TMP/server.log" >&2
    exit 1
  fi
  sleep 0.1
done
if [[ "$ready" -ne 1 ]]; then
  echo "server did not become healthy" >&2
  exit 1
fi

python3 - "$ADDR" "$TMP" <<'PY'
import json, subprocess, sys
from urllib.parse import quote

addr, tmp = sys.argv[1], sys.argv[2]
base = f"http://{addr}"
token = "smoke-token"

def curl(args):
    p = subprocess.run(["curl", "-sS", "-w", "\n%{http_code}", *args], check=True, capture_output=True, text=True)
    body, _, code = p.stdout.rpartition("\n")
    return int(code), body

code, body = curl(["-F", f"image=@{tmp}/bill.png", f"{base}/api/v1/upload"])
assert code == 401, (code, body)

code, body = curl(["-H", f"Authorization: Bearer {token}", "-F", f"image=@{tmp}/note.txt", f"{base}/api/v1/upload"])
assert code == 400, (code, body)

code, body = curl(["-H", f"Authorization: Bearer {token}", "-F", f"image=@{tmp}/bill.png;type=image/png", f"{base}/api/v1/upload"])
assert code == 201, (code, body)
created = json.loads(body)
assert created["is_bill"] is True
assert created["duplicate"] is False
assert created["stored"] is True
bill = created["bill"]
assert bill["currency"] == "CNY" and bill["amount_cents"] > 0
yuan = f"{bill['amount_cents'] // 100}.{bill['amount_cents'] % 100:02d}"
assert bill["amount"] == yuan, bill
assert bill["category"]
assert "mock extractor" not in body
print("created", bill["id"], bill["category"], bill["amount"])

code, body = curl(["-H", f"Authorization: Bearer {token}", "-F", f"image=@{tmp}/bill.png;type=image/png", f"{base}/api/v1/upload"])
assert code == 200, (code, body)
dup = json.loads(body)
assert dup["duplicate"] is True and dup["stored"] is False
assert dup["bill"]["id"] == bill["id"]

code, body = curl(["-H", f"Authorization: Bearer {token}", "-F", f"image=@{tmp}/blank.png;type=image/png", f"{base}/api/v1/upload"])
assert code == 200, (code, body)
nb = json.loads(body)
assert nb["is_bill"] is False and nb["stored"] is False and nb["reason"] == "图片几乎是纯色"
assert nb["notes"] == nb["reason"]
assert "solid" not in body and "receipt" not in body

cat = quote(bill["category"])
code, body = curl(["-H", f"X-API-Token: {token}", f"{base}/api/v1/bills?from=2026-01-01&to=2026-12-31&category={cat}"])
assert code == 200, (code, body)
listed = json.loads(body)
assert listed["count"] == 1 and listed["bills"][0]["id"] == bill["id"]

code, body = curl(["-H", f"Authorization: Bearer {token}", f"{base}/api/v1/summary?from=2026-01-01&to=2026-12-31&category={cat}"])
assert code == 200, (code, body)
summary = json.loads(body)
assert summary["count"] == 1
assert summary["totals"][0]["amount_cents"] == bill["amount_cents"]
assert summary["by_category"][0]["category"] == bill["category"]
print("summary", json.dumps(summary, ensure_ascii=False))

code, body = curl(["-H", "Authorization: Bearer nope", f"{base}/api/v1/summary"])
assert code == 401, (code, body)

mcp = "smoke-mcp-token"
init = json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}})
code, body = curl(["-H", "Content-Type: application/json", "-H", "Accept: application/json, text/event-stream", "-d", init, f"{base}/mcp"])
assert code == 401, (code, body)
code, body = curl(["-H", f"Authorization: Bearer {token}", "-H", "Content-Type: application/json", "-H", "Accept: application/json, text/event-stream", "-d", init, f"{base}/mcp"])
assert code == 401, (code, body)
code, body = curl(["-H", f"Authorization: Bearer {mcp}", "-H", "Content-Type: application/json", "-H", "Accept: application/json, text/event-stream", "-d", init, f"{base}/mcp"])
assert code == 200, (code, body)
assert json.loads(body)["result"]["protocolVersion"] == "2025-06-18"
call = json.dumps({"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"summarize_spend","arguments":{"from":"2026-01-01","to":"2026-12-31"}}})
code, body = curl(["-H", f"Authorization: Bearer {mcp}", "-H", "Content-Type: application/json", "-H", "Accept: application/json, text/event-stream", "-d", call, f"{base}/mcp"])
assert code == 200, (code, body)
assert "image_path" not in body and "raw_model_json" not in body
summary = json.loads(json.loads(body)["result"]["content"][0]["text"])
assert summary["count"] == 1 and summary["amount"] == bill["amount"], summary
code, body = curl(["-H", f"Authorization: Bearer {mcp}", f"{base}/mcp"])
assert code == 405, (code, body)
print("http ok")
PY

if [[ -n "$PID" ]]; then
  kill "$PID" 2>/dev/null || true
  wait "$PID" 2>/dev/null || true
  PID=""
fi

echo "mcp..."
python3 - "$TMP" <<'PY'
import json, os, subprocess, sys
tmp = sys.argv[1]
proc = subprocess.run(
    [f"{tmp}/bill-snap-mcp"],
    input="\n".join([
        json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}),
        json.dumps({"jsonrpc":"2.0","method":"notifications/initialized"}),
        json.dumps({"jsonrpc":"2.0","id":2,"method":"tools/list"}),
        json.dumps({"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"summarize_spend","arguments":{"from":"2026-01-01","to":"2026-12-31"}}}),
        json.dumps({"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_bills","arguments":{"from":"2026-01-01","to":"2026-12-31"}}}),
    ]) + "\n",
    text=True,
    capture_output=True,
    env=os.environ.copy(),
    check=False,
)
if proc.returncode != 0:
    sys.stderr.write(proc.stderr)
    sys.stderr.write(proc.stdout)
    raise SystemExit(f"mcp exited {proc.returncode}")
lines = [ln for ln in proc.stdout.splitlines() if ln.strip()]
msgs = [json.loads(ln) for ln in lines]
by_id = {m["id"]: m for m in msgs if "id" in m}
names = {t["name"] for t in by_id[2]["result"]["tools"]}
assert names == {"list_bills", "summarize_spend", "list_categories"}, names
summary = json.loads(by_id[3]["result"]["content"][0]["text"])
assert summary["count"] == 1 and summary["amount"], summary
listed = json.loads(by_id[4]["result"]["content"][0]["text"])
assert listed["count"] == 1, listed
assert "image_path" not in by_id[4]["result"]["content"][0]["text"]
print("mcp ok", summary["amount"])
PY

echo "smoke ok"
