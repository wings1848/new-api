#!/usr/bin/env bash
# wings-api multi-protocol acceptance run (#30).
#
# Starts a local wings-api (SQLite, temporary data dir) and a mock upstream that
# answers OpenAI chat/responses, Anthropic messages and Gemini generateContent,
# then runs the protocol matrix. Requires: go, python3, curl.
#
#   ./e2e/multi-protocol/run.sh
#
# Ports: 18081 (wings-api), 19290 (mock) — override with API_PORT/MOCK_PORT.
set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
RUN_DIR=${RUN_DIR:-/tmp/wings-multi-protocol-e2e}
API_PORT=${API_PORT:-18081}
MOCK_PORT=${MOCK_PORT:-19290}
API="http://127.0.0.1:$API_PORT"
MOCK="http://127.0.0.1:$MOCK_PORT"
OUT="$RUN_DIR/out"
BIN="$RUN_DIR/wings-api-e2e"
PIDS=()

rm -rf "$RUN_DIR"
mkdir -p "$RUN_DIR/data" "$OUT"

cleanup() {
  for pid in "${PIDS[@]:-}"; do kill "$pid" >/dev/null 2>&1 || true; done
}
trap cleanup EXIT

# The root module embeds web/dist; a placeholder keeps `go build` happy when the
# frontend was not built (it is gitignored).
mkdir -p "$REPO_ROOT/web/dist"
[ -f "$REPO_ROOT/web/dist/index.html" ] || : > "$REPO_ROOT/web/dist/index.html"
( cd "$REPO_ROOT" && GOWORK=off go build -o "$BIN" . ) || { echo "build failed"; exit 1; }

python3 "$REPO_ROOT/e2e/multi-protocol/mock_upstream.py" "$MOCK_PORT" >"$OUT/mock.log" 2>&1 &
PIDS+=($!)
( cd "$RUN_DIR/data" && exec env PORT="$API_PORT" SESSION_SECRET=e2e-session-secret LOG_LEVEL=error "$BIN" >"$OUT/server.log" 2>&1 ) &
PIDS+=($!)

for _ in $(seq 1 60); do
  curl -fsS "$API/api/status" >/dev/null 2>&1 && break
  sleep 1
done
curl -fsS "$API/api/status" >/dev/null || { echo "server did not start"; tail -20 "$OUT/server.log"; exit 1; }

json_get() { python3 -c "import json,sys;d=json.load(sys.stdin);print(eval(sys.argv[1],{'d':d}))" "$1"; }

curl -sS -X POST "$API/api/setup" -H 'Content-Type: application/json' \
  -d '{"username":"root","password":"Passw0rd!","confirmPassword":"Passw0rd!","SelfUseModeEnabled":true,"DemoSiteEnabled":false}' > "$OUT/setup.json"
COOKIES="$RUN_DIR/cookies.txt"
curl -sS -c "$COOKIES" -X POST "$API/api/user/login" -H 'Content-Type: application/json' \
  -d '{"username":"root","password":"Passw0rd!"}' > "$OUT/login.json"
AT=$(json_get "d['data']['access_token']" < "$OUT/login.json")
AUTH=(-H "Authorization: Bearer $AT")
curl -sS -b "$COOKIES" "${AUTH[@]}" -X POST "$API/api/user/manage" -H 'Content-Type: application/json' \
  -d '{"id":1,"action":"set_unlimited_quota","value":1}' > "$OUT/user-unlimited.json"

add_channel() {
  local type=$1 name=$2 key=$3 models=$4
  # -L follows Gin's 307 for the trailing-slash redirect of /api/channel.
  curl -sS -L -b "$COOKIES" "${AUTH[@]}" -X POST "$API/api/channel" -H 'Content-Type: application/json' \
    -d "{\"mode\":\"single\",\"channel\":{\"type\":$type,\"name\":\"$name\",\"key\":\"$key\",\"base_url\":\"$MOCK\",\"models\":\"$models\",\"group\":\"default\"}}" > "$OUT/channel-$name.json"
}
add_channel 1 mock-openai sk-mock-openai "mock-gpt,mock-429,mock-500"
add_channel 14 mock-anthropic sk-mock-anthropic mock-claude
add_channel 24 mock-gemini sk-mock-gemini mock-gemini

curl -sS -L -b "$COOKIES" "${AUTH[@]}" -X POST "$API/api/token" -H 'Content-Type: application/json' \
  -d '{"name":"e2e","unlimited_quota":true,"remain_quota":0,"expired_time":-1,"group":"default"}' > "$OUT/token-create.json"
TOKEN_ID=$(curl -sS -L -b "$COOKIES" "${AUTH[@]}" "$API/api/token/?p=1&page_size=10" | json_get "d['data']['items'][0]['id']") || { echo "no token"; exit 1; }
KEY=$(curl -sS -L -b "$COOKIES" "${AUTH[@]}" -X POST "$API/api/token/$TOKEN_ID/key" | json_get "d['data']['key']")
echo "$KEY" > "$OUT/token.key"

relay() { # name method path body extra curl args...
  local name=$1 method=$2 path=$3 body=$4; shift 4
  local code
  code=$(curl -sS -o "$OUT/$name.body" -w '%{http_code}' -X "$method" "$API$path" \
    -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' "$@" -d "$body")
  echo "$code" > "$OUT/$name.code"
}

relay chat-nonstream POST /v1/chat/completions '{"model":"mock-gpt","messages":[{"role":"user","content":"ping"}],"max_tokens":16}'
relay chat-stream POST /v1/chat/completions '{"model":"mock-gpt","messages":[{"role":"user","content":"ping"}],"max_tokens":16,"stream":true}'
relay chat-tools POST /v1/chat/completions '{"model":"mock-gpt","messages":[{"role":"user","content":"weather?"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}'
relay chat-image POST /v1/chat/completions '{"model":"mock-gpt","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"https://example.com/x.png"}}]}]}'
relay responses POST /v1/responses '{"model":"mock-gpt","input":"ping"}'
relay anthropic POST /v1/messages '{"model":"mock-claude","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}' -H 'anthropic-version: 2023-06-01'
relay gemini POST "/v1beta/models/mock-gemini:generateContent?key=$KEY" '{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}'
relay responses-stream POST /v1/responses '{"model":"mock-gpt","input":"ping","stream":true}'
relay anthropic-stream POST /v1/messages '{"model":"mock-claude","max_tokens":16,"messages":[{"role":"user","content":"ping"}],"stream":true}' -H 'anthropic-version: 2023-06-01'
relay gemini-stream POST "/v1beta/models/mock-gemini:streamGenerateContent?key=$KEY" '{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}'
relay chat-tool-roundtrip POST /v1/chat/completions '{"model":"mock-gpt","messages":[{"role":"user","content":"weather?"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_mock","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Shenzhen\"}"}}]},{"role":"tool","tool_call_id":"call_mock","content":"sunny"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}'
relay upstream-429 POST /v1/chat/completions '{"model":"mock-429","messages":[{"role":"user","content":"ping"}]}'
relay upstream-500 POST /v1/chat/completions '{"model":"mock-500","messages":[{"role":"user","content":"ping"}]}'

python3 - "$OUT" <<'PY'
import json, os, sys
out = sys.argv[1]
checks = []
def load(name):
    with open(os.path.join(out, name + '.code')) as f:
        code = int(f.read().strip())
    with open(os.path.join(out, name + '.body')) as f:
        body = f.read()
    return code, body

code, body = load('chat-nonstream'); checks.append(('openai chat non-stream', code == 200 and 'pong' in body, code))
code, body = load('chat-stream'); compact = body.replace(' ', '')
checks.append(('openai chat stream', code == 200 and '[DONE]' in body and '"content":"po"' in compact and '"content":"ng"' in compact, code))
code, body = load('chat-tools'); checks.append(('openai chat tools', code == 200 and 'tool_calls' in body and 'get_weather' in body, code))
code, body = load('chat-image'); checks.append(('openai chat image', code == 200 and 'pong' in body, code))
code, body = load('responses'); checks.append(('openai responses', code == 200 and ('pong' in body or 'output' in body), code))
code, body = load('anthropic'); checks.append(('anthropic messages', code == 200 and ('pong' in body or 'message' in body), code))
code, body = load('gemini'); checks.append(('gemini generateContent', code == 200 and ('pong' in body or 'candidates' in body), code))
code, body = load('responses-stream'); checks.append(('openai responses stream', code == 200 and 'response.output_text.delta' in body and 'po' in body and 'ng' in body, code))
code, body = load('anthropic-stream'); checks.append(('anthropic stream', code == 200 and 'content_block_delta' in body and 'text_delta' in body, code))
code, body = load('gemini-stream'); checks.append(('gemini stream', code == 200 and 'candidates' in body and 'po' in body and 'ng' in body, code))
code, body = load('chat-tool-roundtrip'); checks.append(('tool result round-trip', code == 200 and 'pong' in body, code))
code, body = load('upstream-429'); checks.append(('upstream 429 surfaced', 400 <= code < 500, code))
code, body = load('upstream-500'); checks.append(('upstream 500 surfaced', code >= 400, code))
fail = 0
for name, ok, code in checks:
    print(('PASS' if ok else 'FAIL'), name, 'http=' + str(code))
    fail += 0 if ok else 1
print('SUMMARY', len(checks) - fail, '/', len(checks), 'passed')
raise SystemExit(0 if fail == 0 else 1)
PY
