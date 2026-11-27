#!/bin/bash
set -euo pipefail
umask 077

repo=$(cd "$(dirname "$0")/../../.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/dex-apple-qa.XXXXXXXX")
cd "$repo"
children=()
cleanup() {
  if ((${#children[@]})); then
    kill "${children[@]}" 2>/dev/null || true
    wait "${children[@]}" 2>/dev/null || true
  fi
  rm -rf -- "$work"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Alternate ports let the app switch between two HTTPS origins.
http_port=${QA_HTTP_PORT:-19187}
backend_port=${QA_BACKEND_PORT:-19188}
https_port=${QA_HTTPS_PORT:-19443}
alt_port=${QA_ALT_PORT:-19444}
open_port=${QA_OPEN_PORT:-19445}
nav_port=${QA_NAV_PORT:-19446}
for port in "$http_port" "$backend_port" "$https_port" "$alt_port" "$open_port" "$nav_port"; do
  if [[ ! $port =~ ^[1-9][0-9]{0,4}$ ]] || ((port < 1024 || port > 65535)); then
    printf 'Invalid QA port: %s\n' "$port" >&2
    exit 1
  fi
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null; then
    printf 'QA port in use: %s\n' "$port" >&2
    exit 1
  fi
done
if [[ $(printf '%s\n' "$http_port" "$backend_port" "$https_port" "$alt_port" "$open_port" "$nav_port" | sort -u | wc -l) -ne 6 ]]; then
  printf 'Use six different QA ports.\n' >&2
  exit 1
fi

mise run build:web
# The test build enables the faux provider and disables login shell discovery.
mise exec -- go build -tags dex_e2e -o "$work/dex" ./cmd/dex
mkdir "$work/http-home" "$work/tls-home" "$work/proxy-home"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -sha256 \
  -subj '/CN=Dex Apple QA Root' \
  -addext 'basicConstraints=critical,CA:TRUE,pathlen:0' \
  -addext 'keyUsage=critical,keyCertSign,cRLSign' \
  -keyout "$work/ca.key" -out "$work/ca.pem" 2> "$work/cert.log"
openssl req -new -newkey rsa:2048 -nodes -subj '/CN=127.0.0.1' \
  -keyout "$work/tls.key" -out "$work/tls.csr" 2>> "$work/cert.log"
printf '%s\n' 'subjectAltName=IP:127.0.0.1' 'extendedKeyUsage=serverAuth' \
  'basicConstraints=critical,CA:FALSE' 'keyUsage=critical,digitalSignature,keyEncipherment' > "$work/tls.ext"
openssl x509 -req -in "$work/tls.csr" -CA "$work/ca.pem" -CAkey "$work/ca.key" \
  -CAcreateserial -days 1 -sha256 -extfile "$work/tls.ext" -out "$work/tls.pem" 2>> "$work/cert.log"

proxy=$(command -v caddy)
printf '{\n  admin off\n  auto_https off\n}\n' > "$work/Caddyfile"
mkdir "$work/nav"
printf '<!doctype html><meta name="viewport" content="width=device-width, initial-scale=1">\n<p><a href="https://127.0.0.1:%s/qa-external">Ordinary outside link</a></p>\n<button onclick="location.assign(\x27https://127.0.0.1:%s/qa-oauth?state=qa\x27)">Start scripted OAuth</button>\n' \
  "$alt_port" "$alt_port" > "$work/nav/index.html"
for port in "$https_port" "$alt_port" "$open_port" "$nav_port"; do
  upstream=$backend_port
  [[ "$port" != "$open_port" ]] || upstream=$http_port
  printf '\nhttps://127.0.0.1:%s {\n  bind 127.0.0.1\n  tls "%s/tls.pem" "%s/tls.key"\n' \
    "$port" "$work" "$work" >> "$work/Caddyfile"
  if [[ "$port" == "$nav_port" ]]; then
    printf '  handle / {\n    root * "%s/nav"\n    file_server\n  }\n' "$work" >> "$work/Caddyfile"
  fi
  printf '  handle {\n    reverse_proxy 127.0.0.1:%s {\n      header_down Set-Cookie "^(dex_session=.*)$" "$1; Secure"\n    }\n  }\n}\n' "$upstream" >> "$work/Caddyfile"
done

start_dex() {
  local home=$1 port=$2
  shift 2
  env -i HOME="$home" PATH=/usr/bin:/bin SHELL=/bin/sh DEX_FAKE_MODEL=1 \
    "$work/dex" serve --host 127.0.0.1 --port "$port" --data-dir "$home/data" "$@" > "$home/dex.log" 2>&1 &
  children+=("$!")
}
start_dex "$work/http-home" "$http_port"
start_dex "$work/tls-home" "$backend_port" --require-token --stored-token
env -i HOME="$work/proxy-home" PATH=/usr/bin:/bin "$proxy" run --config "$work/Caddyfile" --adapter caddyfile > "$work/proxy.log" 2>&1 &
children+=("$!")

request=(env -i PATH=/usr/bin:/bin curl -q --silent --show-error --noproxy '*' --max-time 5)
local_url="http://127.0.0.1:$http_port"
https_url="https://127.0.0.1:$https_port"
alt_url="https://127.0.0.1:$alt_port"
open_url="https://127.0.0.1:$open_port"
nav_url="https://127.0.0.1:$nav_port"
await_status() {
  local expected=$1 actual deadline=$((SECONDS + 20))
  shift
  until actual=$("${request[@]}" -o /dev/null -w '%{http_code}' "$@" 2>/dev/null) && [[ "$actual" == "$expected" ]]; do
    if ((SECONDS >= deadline)); then
      printf 'QA readiness failed: expected %s, last status %s\n' "$expected" "$actual" >&2
      exit 1
    fi
    for pid in "${children[@]}"; do kill -0 "$pid"; done
    sleep 0.2
  done
}
await_status 200 "$local_url/api/health"
await_status 401 "http://127.0.0.1:$backend_port/api/health"
await_status 401 --cacert "$work/ca.pem" "$https_url/api/health"
await_status 401 --cacert "$work/ca.pem" "$alt_url/api/health"
await_status 200 --cacert "$work/ca.pem" "$open_url/api/health"
await_status 401 --cacert "$work/ca.pem" "$nav_url/api/health"
await_status 200 --cacert "$work/ca.pem" "$nav_url/"
printf 'Unauthenticated /api/health: local=200, token upstream=401, token HTTPS=401, no-token HTTPS=200\n'
env -i HOME="$work/tls-home" PATH=/usr/bin:/bin "$work/dex" token \
  --data-dir "$work/tls-home/data" > "$work/token"
printf 'Authorization: Bearer %s\n' "$(< "$work/token")" > "$work/auth-header"
printf '{"token":"%s"}\n' "$(< "$work/token")" > "$work/login.json"
for url in "$https_url" "$alt_url"; do
  status=$("${request[@]}" --cacert "$work/ca.pem" -D "$work/login-headers" -o /dev/null -w '%{http_code}' \
    -H 'Content-Type: application/json' --data @"$work/login.json" "$url/api/session")
  [[ "$status" == 204 ]]
  grep -i '^Set-Cookie: dex_session=' "$work/login-headers" | grep -Ei '; Secure([;[:space:]]|$)' | grep -Ei '; HttpOnly([;[:space:]]|$)' | grep -Eiq '; SameSite=Strict([;[:space:]]|$)'
done
rm -f "$work/login.json" "$work/login-headers"
printf 'Both token HTTPS origins: 204 login with Secure, HttpOnly and SameSite=Strict\n'
for url in "$local_url" "$https_url" "$alt_url"; do
  args=()
  if [[ "$url" == https:* ]]; then
    args=(--cacert "$work/ca.pem" -H "@$work/auth-header")
  fi
  "${request[@]}" -f "${args[@]+"${args[@]}"}" -X PUT \
    -H 'Content-Type: application/json' \
    -d '{"name":"Dex QA","provider":"faux","modelId":"echo","thinkingLevel":"off"}' \
    "$url/api/bots/default" >/dev/null
  "${request[@]}" -f "${args[@]+"${args[@]}"}" "$url/api/bots/default" \
    | jq -e '.provider == "faux" and .modelId == "echo"' >/dev/null
done
printf 'QA_READY\nFixture: %s\nLocal: %s\nTLS primary: %s\nTLS alternate: %s\nTLS no token: %s\nTLS navigation: %s\n' \
  "$work" "$local_url" "$https_url" "$alt_url" "$open_url" "$nav_url"
while :; do
  for pid in "${children[@]}"; do
    if ! kill -0 "$pid" 2>/dev/null; then
      printf 'A QA service stopped unexpectedly.\n' >&2
      exit 1
    fi
  done
  sleep 1
done
