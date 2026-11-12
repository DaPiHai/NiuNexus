// Package webui embeds the browser assets served by niunexus-web.
package webui

import "embed"

// FS contains the built-in UI. It is embedded so the app has no runtime
// dependency on a Node toolchain or an external asset directory.
//
//go:embed assets
var FS embed.FS
