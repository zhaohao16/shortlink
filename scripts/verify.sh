#!/usr/bin/env bash
# 端到端验收脚本：需要服务已在 BASE 地址运行（本地 go run 或 docker compose）。
set -euo pipefail

BASE="${BASE:-http://localhost:8080}"
TARGET='https://example.com/a/very/long/path?x=1&y=2'

fail() { echo "FAIL: $*" >&2; exit 1; }

echo "== 1. 健康检查"
body=$(curl -fsS "$BASE/healthz")
case "$body" in
  *'"status":"ok"'*) echo "   $body" ;;
  *) fail "healthz = $body" ;;
esac

echo "== 2. 创建短链"
resp=$(curl -fsS -X POST "$BASE/api/v1/shorten" \
  -H 'Content-Type: application/json' \
  --data-binary "{\"url\":\"$TARGET\"}")
code=$(printf '%s' "$resp" | sed -n 's/.*"code":"\([^"]*\)".*/\1/p')
[ "${#code}" -eq 6 ] || fail "bad code: $code"
echo "   code=$code"

echo "== 3. 跳转"
loc=$(curl -sS -o /dev/null -w '%{redirect_url}' "$BASE/$code")
[ "$loc" = "$TARGET" ] || fail "location = $loc"
echo "   location=$loc"

echo "== 4. 非法 URL 被拒"
status=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$BASE/api/v1/shorten" \
  -H 'Content-Type: application/json' -d '{"url":"javascript:alert(1)"}')
[ "$status" = "400" ] || fail "want 400 got $status"

echo "== 5. 不存在的短码"
status=$(curl -sS -o /dev/null -w '%{http_code}' "$BASE/000000")
[ "$status" = "404" ] || fail "want 404 got $status"

echo "== 6. API 路径不被短码吞掉"
status=$(curl -sS -o /dev/null -w '%{http_code}' "$BASE/api/v1/shorten")
[ "$status" = "405" ] || fail "want 405 got $status"

echo "== 7. 并发创建 1000 条，短码必须无重复"
# 每个请求独立落盘：并发写同一管道会造成响应交错、计数失真
tmpd=$(mktemp -d)
seq 1 1000 | xargs -P 32 -I@ curl -sS -o "$tmpd/@.json" \
  -X POST "$BASE/api/v1/shorten" \
  -H 'Content-Type: application/json' \
  --data-binary '{"url":"https://example.com/concurrent/@"}'

total=$(find "$tmpd" -name '*.json' | wc -l | tr -d ' ')
ok=$(grep -l '"code":"' "$tmpd"/*.json 2>/dev/null | wc -l | tr -d ' ')
uniq=$(grep -ho '"code":"[^"]*"' "$tmpd"/*.json 2>/dev/null | sort -u | wc -l | tr -d ' ')
echo "   total=$total created=$ok unique=$uniq"

if [ "$ok" != "1000" ]; then
  echo "   首个失败响应：$(grep -L '"code":"' "$tmpd"/*.json 2>/dev/null | head -1 | xargs -r cat)"
fi
rm -rf "$tmpd"
[ "$total" = "1000" ] || fail "only $total responses"
[ "$ok" = "1000" ] || fail "only $ok created"
[ "$uniq" = "1000" ] || fail "duplicate codes detected"

echo "== ALL PASSED"
