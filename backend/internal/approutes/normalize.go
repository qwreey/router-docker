package approutes

import (
	"os"
	"path/filepath"
	"strings"
)

// Normalize is App Routes' half of the same startup repair devproxy.Normalize
// does - see that function's doc comment for why re-rendering matters
// (the pre-2026-08-27 tinyauth forward_auth block never worked, and a
// fragment is otherwise only rewritten when a user happens to edit it).
//
// This is the half the VNC tab actually depends on: a VNC target IS an App
// Route (internal/vnc delegates the whole proxying half here), so a target
// registered with 인증 요구 before the fix is repaired by this on the next
// router start rather than needing the user to re-save it.
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
		a, ok := parseStructured(name, string(data))
		if !ok {
			continue
		}
		rendered := Render(a)
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
