// Package web embeds the static demo page and browser client bundle
// that the Go binary serves. p2p-client.js is a placeholder until the
// TypeScript client build copies its browser bundle here (see
// clients/ts); the Go build then embeds the real artifact.
package web

import "embed"

//go:embed index.html demo.js p2p-client.js
var Files embed.FS
