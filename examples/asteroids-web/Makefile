# Build the browser game. The only tool is the TypeScript compiler that
# already ships with the lobbylink TS client, so there is nothing new to
# install. `make` produces dist/*.js and copies the lobbylink browser
# bundle next to them as dist/p2p-client.js.

CLIENT := ../../clients/ts
TSC := $(CLIENT)/node_modules/.bin/tsc

.PHONY: all clean serve check
all: dist/game.js dist/p2p-client.js

$(TSC):
	cd $(CLIENT) && npm install

$(CLIENT)/dist/index.js:
	$(MAKE) -C $(CLIENT)

dist/p2p-client.js: $(CLIENT)/dist/index.js
	mkdir -p dist
	cp $(CLIENT)/dist/index.js dist/p2p-client.js

dist/game.js: src/game.ts src/protocol.ts src/p2p-client.d.ts tsconfig.json $(CLIENT)/dist/index.js | $(TSC)
	$(TSC) -p .

check: | $(TSC)
	$(TSC) -p . --noEmit

serve: all
	@echo "open http://localhost:5173/  (localhost:5173 is an allowlisted origin on prod)"
	python3 -m http.server 5173

clean:
	rm -rf dist
