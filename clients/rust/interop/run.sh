#!/bin/sh
# Runs the full client interop matrix on a local lobby server:
#   wasm<->wasm, native<->wasm, TS<->wasm, TS<->native
# (native<->native is covered by `cargo test` in clients/rust).
#
# Requires: dist/p2p-lobby-server at the repo root, wasm-pack,
# google-chrome, python3. Pass a server URL to run the browser suites
# against a different (e.g. production) server; the page is always
# served from http://localhost:5173, which prod allowlists.
set -eu
cd "$(dirname "$0")"

SERVER="${1:-http://127.0.0.1:8971}"
REPO_ROOT="$(cd ../../.. && pwd)"
WORK="$(mktemp -d /tmp/lobbylink-interop.XXXXXX)"
CHROME_FLAGS="--headless=new --disable-gpu --no-sandbox --enable-logging=stderr"
RUN_ID=$$
trap 'kill $(cat "$WORK"/*.pid 2>/dev/null) 2>/dev/null || true' EXIT

echo "== building wasm harness + native driver"
(cd wasmtest && wasm-pack build --target web --dev >/dev/null 2>&1)
(cd driver && cargo build -q)
mkdir -p "$WORK/serve"
cp serve/index.html "$WORK/serve/"
cp "$REPO_ROOT/web/p2p-client.js" "$WORK/serve/"
cp -r wasmtest/pkg "$WORK/serve/"

if [ "$SERVER" = "http://127.0.0.1:8971" ]; then
    echo "== starting local lobby server"
    "$REPO_ROOT/dist/p2p-lobby-server" --listen-http 127.0.0.1:8971 \
        --allow-no-origin --allowed-origin http://localhost:5173 \
        >"$WORK/server.log" 2>&1 &
    echo $! > "$WORK/server.pid"
    DRIVER_ORIGIN="--no-origin"
else
    DRIVER_ORIGIN=""
fi
(cd "$WORK/serve" && python3 -m http.server 5173 --bind 127.0.0.1 \
    >"$WORK/http.log" 2>&1 &
    echo $! > "$WORK/http.pid")
sleep 1

page() {
    # page <mode> <code> -> URL
    printf 'http://localhost:5173/index.html?mode=%s&code=%s&server=' "$1" "$2"
    python3 -c "import urllib.parse,sys;print(urllib.parse.quote(sys.argv[1],safe=''))" "$SERVER"
}

fail=0

echo "== wasm<->wasm (pair suite)"
timeout 120 google-chrome $CHROME_FLAGS --user-data-dir="$WORK/cp1" \
    "$(page pair WWPAIR$RUN_ID)" >"$WORK/pair.log" 2>&1 || true
grep -q "TESTLOG ALL-TESTS-PASS" "$WORK/pair.log" \
    && echo PASS || { echo "FAIL (see $WORK/pair.log)"; fail=1; }

echo "== native<->wasm (wasm echo + native driver)"
timeout 180 google-chrome $CHROME_FLAGS --user-data-dir="$WORK/cp2" \
    "$(page echo NWECHO$RUN_ID)" >"$WORK/echo.log" 2>&1 &
echo $! > "$WORK/chrome2.pid"
./driver/target/debug/interop-driver "$SERVER" "NWECHO$RUN_ID" $DRIVER_ORIGIN \
    >"$WORK/driver1.log" 2>&1 \
    && echo PASS || { echo "FAIL (see $WORK/driver1.log)"; fail=1; }

echo "== TS<->wasm (TS echo + wasm driver, one page)"
timeout 120 google-chrome $CHROME_FLAGS --user-data-dir="$WORK/cp3" \
    "$(page tswasm TWPAIR$RUN_ID)" >"$WORK/tswasm.log" 2>&1 || true
grep -q "TESTLOG DRIVER-ALL-PASS" "$WORK/tswasm.log" \
    && echo PASS || { echo "FAIL (see $WORK/tswasm.log)"; fail=1; }

echo "== TS<->native (TS echo + native driver)"
timeout 180 google-chrome $CHROME_FLAGS --user-data-dir="$WORK/cp4" \
    "$(page tsecho TNECHO$RUN_ID)" >"$WORK/tsecho.log" 2>&1 &
echo $! > "$WORK/chrome4.pid"
./driver/target/debug/interop-driver "$SERVER" "TNECHO$RUN_ID" $DRIVER_ORIGIN \
    >"$WORK/driver2.log" 2>&1 \
    && echo PASS || { echo "FAIL (see $WORK/driver2.log)"; fail=1; }

if [ "$fail" = 0 ]; then
    echo "== INTEROP MATRIX PASS =="
    rm -rf "$WORK"
else
    echo "== INTEROP MATRIX FAILED (logs in $WORK) =="
fi
exit $fail
