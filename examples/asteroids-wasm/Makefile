# Build the Rust/wasm browser game and serve it.
#
#   make          wasm-pack build -> serve/pkg/
#   make serve    build + python3 -m http.server 5173  (an allowlisted origin
#                 on the prod test server, so P2P connects work headless too)
#
# Requires wasm-pack (cargo install wasm-pack) and the wasm32 target
# (rustup target add wasm32-unknown-unknown).

.PHONY: all serve clean

all: serve/pkg/asteroids_wasm.js

serve/pkg/asteroids_wasm.js: src/lib.rs Cargo.toml ../asteroids-native/src/protocol.rs
	wasm-pack build --target web --out-dir serve/pkg --dev

serve: all
	@echo "open http://localhost:5173/"
	cd serve && python3 -m http.server 5173

clean:
	rm -rf serve/pkg target
