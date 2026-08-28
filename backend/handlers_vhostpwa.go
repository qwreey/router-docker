package main

import (
	"log"
	"net/http"

	"router/internal/vhostpwa"
)

// handleVhostManifest serves the rewritten PWA manifest for a vhost-published
// app — see internal/vhostpwa for why an app published here may need one.
//
// Registered without the password gate, and it has to be: the manifest and its
// icon are fetched by whoever is installing the app, including Google's WebAPK
// build servers, which carry no session of any kind. It exposes only what the
// operator wrote into this container's own environment (an app name) plus the
// app's own already-public manifest, and takes no caller-supplied URL — the
// upstream comes from the ROUTER_VHOST_<NAME> entry that publishes the
// hostname in the first place, so this is not a fetch primitive.
//
// Any failure answers a bare 502, which the generated nginx block turns into a
// fallback to the app's own unmodified manifest: worst case the PWA installs
// under the app's own name instead of the configured one, rather than not
// installing at all.
func handleVhostManifest(w http.ResponseWriter, r *http.Request) {
	o, ok := vhostpwa.Lookup(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	patched, err := vhostpwa.Patch(o)
	if err != nil {
		log.Printf("handleVhostManifest: %v", err)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	// The name/icon can change with a container recreate, and an installed PWA
	// only re-reads this occasionally as it is — no reason to let a proxy hold
	// a stale copy on top of that.
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(patched)
}
