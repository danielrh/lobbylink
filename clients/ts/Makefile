# lobbylink TypeScript client. The entire build is one tsc invocation;
# the only tool fetched by npm is the TypeScript compiler itself.
# `make` produces dist/ (npm package output) and refreshes the browser
# bundle ../../web/p2p-client.js that the Go binary embeds.

TSC = node_modules/.bin/tsc

all: ../../web/p2p-client.js

../../web/p2p-client.js: dist/index.js
	cp dist/index.js $@

dist/index.js: src/index.ts tsconfig.json $(TSC)
	$(TSC) -p .

$(TSC):
	npm install

# Type-check without emitting.
check: $(TSC)
	$(TSC) -p . --noEmit

clean:
	rm -rf dist

.PHONY: all check clean
