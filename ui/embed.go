//go:build !no_ui

// Package ui embeds scry's built status dashboard.
package ui

import "embed"

//go:embed all:dist
var FS embed.FS
