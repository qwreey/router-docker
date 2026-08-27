package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// staticHandler serves router/frontend's pre-built SPA assets from dir. Any
// GET path that doesn't map to a real file falls back to index.html (client-
// side routing - the SPA itself has none today, but this keeps a reload on
// any sub-path working regardless) - and if dir/index.html doesn't exist
// either (e.g. the frontend hasn't been built into place yet), it 404s
// instead of panicking. Ported verbatim from webmanager/backend/static.go.
//
// index.html always gets Cache-Control: no-cache (forces revalidation, but
// still cacheable/conditional-GET-able) - it's the one file whose content
// changes on every rebuild (new hashed asset filenames) while keeping the
// same URL, so letting a browser cache it beyond that would leave it
// referencing assets a later `docker compose build` has already deleted.
// Confirmed live: after a rebuild changed the frontend's asset hashes, a
// browser with a stale cached index.html requested the old
// index-<hash>.css, missed on disk, fell through to this handler's own
// SPA-fallback branch below, and got index.html's HTML back with a
// text/html Content-Type where the <link> tag expected CSS - exactly the
// "stylesheet not loaded, MIME type text/html" error this fixes. Files
// under assets/ are Vite's hashed, content-addressed bundle output (a
// changed file always gets a new filename), so those are safe to cache
// indefinitely.
func staticHandler(dir string) http.Handler {
	fileServer := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if info, err := os.Stat(requested); err == nil && !info.IsDir() {
			if strings.HasPrefix(filepath.ToSlash(r.URL.Path), "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		index := filepath.Join(dir, "index.html")
		if _, err := os.Stat(index); err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}

// novncHandler serves router's own vendored copy of noVNC (see the
// Dockerfile's NOVNC_VERSION stage) for BackendRFB targets - the viewer
// half of what internal/vnc's package doc calls "router owning the client
// side". It is a plain file server with two deliberate differences from
// staticHandler above:
//
//   - no SPA fallback. A missing file here is a missing file, not a route;
//     falling back to index.html would answer a bad asset request with
//     HTML and produce the same MIME-type confusion staticHandler's own
//     comment describes.
//   - any path ending in "/" is refused, which is what turns off
//     http.FileServer's directory listings (noVNC ships no index.html at
//     its root, so /novnc/ would otherwise enumerate its whole tree). Every
//     URL the VNC tab produces names vnc.html explicitly, so nothing legit
//     ends in a slash. The empty path is the same case and has to be
//     spelled out separately: http.StripPrefix("/novnc/") turns a request
//     for exactly "/novnc/" into "", which has no trailing slash to test - and
//     that is precisely the request that lists the whole tree.
//
// Deliberately NOT behind the auth gate even when one is configured: this
// is upstream noVNC's own static JS and HTML, identical for every
// deployment and carrying nothing about this one. The socket it tries to
// open (handleVncSocket) is the gate.
func novncHandler(dir string) http.Handler {
	fileServer := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
