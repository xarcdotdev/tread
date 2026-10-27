// Package web embeds the dashboard and the control page.
package web

import "embed"

//go:embed *.html *.js
var FS embed.FS
