// Type-only shim: at runtime `make` copies the compiled lobbylink browser
// bundle to dist/p2p-client.js next to this game, so the emitted
// `import ... from "./p2p-client.js"` resolves in the browser. For
// type-checking, point tsc at the client's declaration file.
export * from "../../../clients/ts/dist/index";
