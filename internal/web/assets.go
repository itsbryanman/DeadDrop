// Package web embeds the browser upload page.
package web

import _ "embed"

//go:embed static/index.html
var IndexHTML []byte

//go:embed static/app.css
var AppCSS []byte

//go:embed static/app.js
var AppJS []byte
