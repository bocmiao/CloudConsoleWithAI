// Package webui embeds the browser UI. It needs no build step: Vue 3 is
// vendored as its global production build (static/vue.global.prod.js, MIT).
package webui

import "embed"

// Static holds index.html and its assets under "static/".
//
//go:embed static
var Static embed.FS
