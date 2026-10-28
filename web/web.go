// Package web embeds the dashboard and the control page.
package web

import "embed"

//go:embed *.html *.js *.css *.svg
var FS embed.FS
