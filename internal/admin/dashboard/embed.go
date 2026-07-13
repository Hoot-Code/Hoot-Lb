package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
)

// staticFS embeds the dashboard's HTML, CSS, and JS assets at build
// time. Nothing the dashboard loads comes from the network at
// runtime — no CDN fonts, icons, or JS libraries — so the tool works
// fully offline, consistent with this project's self-hosted
// positioning.
//
//go:embed static
var staticFS embed.FS

// AssetHandler returns an http.Handler that serves the dashboard's
// embedded static assets (index.html, style.css, app.js) rooted at
// "/". The returned handler requires no authentication — it serves
// only static markup and script, never backend state — matching the
// architecture decision that loading the dashboard page itself is
// unauthenticated while every data-fetching call it makes is not.
func AssetHandler() (http.Handler, error) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	return http.FileServer(http.FS(sub)), nil
}
