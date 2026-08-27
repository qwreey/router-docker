package devproxy

import (
	"os"
	"path/filepath"
	"strings"
)

// Normalize re-renders every managed fragment that still round-trips
// through parseStructured but whose text no longer matches what Render
// produces, and returns the names it rewrote.
//
// This exists because the fragment template is not just cosmetic: the
// tinyauth forward_auth block shipped before 2026-08-27 was *functionally
// broken* (see forwardauth.go's doc comment - every auth-required request
// 400'd, and an unauthenticated one never reached a login page). Fragments
// are written once and then only touched again when a user happens to edit
// that route, so without this an existing deployment would keep serving the
// broken block forever and the fix would silently only reach new entries.
//
// Only fragments that parse structurally are touched - a raw-editor
// fragment (parseStructured returns !ok) is a user's own text and is left
// exactly as-is. The rewrite is byte-identical to what saving that same
// entry from the UI would produce, so it can never introduce a shape
// Create/Update wouldn't have written itself.
func Normalize() ([]string, error) {
	entries, err := os.ReadDir(ManagedDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var changed []string
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".caddy") {
			continue
		}
		name := strings.TrimSuffix(ent.Name(), ".caddy")
		p := filepath.Join(ManagedDir, ent.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		e, ok := parseStructured(name, string(data))
		if !ok {
			continue
		}
		rendered := Render(e)
		if rendered == string(data) {
			continue
		}
		if err := os.WriteFile(p, []byte(rendered), 0o644); err != nil {
			return changed, err
		}
		changed = append(changed, name)
	}
	return changed, nil
}
