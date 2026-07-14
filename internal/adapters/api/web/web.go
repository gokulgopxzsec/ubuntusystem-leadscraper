// Package web serves the dashboard.
//
// The UI is embedded into the binary with go:embed rather than shipped as
// loose files. That keeps deployment to a single artifact: no Node, no npm, no
// build step, no static volume to mount, and nothing extra to run on a box with
// two cores and very little spare memory.
package web

import (
	"bytes"
	"net/http"
	"time"

	_ "embed"
)

//go:embed index.html
var index []byte

// Handler serves the dashboard. Everything it needs already exists on /api/v1,
// so this is purely additive.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		// The API is the source of truth and the page is tiny, so don't let the
		// browser cache it. A stale UI against a fresh API is confusing to debug.
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	})
}
