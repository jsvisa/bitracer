// Package web embeds the built dashboard (web/dist) into the binary.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
