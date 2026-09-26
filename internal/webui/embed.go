// Package webui embeds the browser UI. It needs no build step: Vue 3 is
// vendored as its global production build (static/vue.global.prod.js, MIT),
// and so is xterm.js with its fit addon for the terminal (static/xterm/,
// MIT, see static/xterm/LICENSE).
package webui

import "embed"

// Static holds index.html and its assets under "static/".
//
//go:embed static
var Static embed.FS
