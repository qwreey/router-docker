// Package atomicfile provides a crash-safe file write (temp file in the
// same directory, fsync, rename, fsync the directory) for every file
// router-manager read-modify-writes: credentials (authgate,
// tinyauthusers), Caddy fragments, and the configs that polling scripts
// read (netgate, tailscale, dns). A torn write there would be a truncated
// config read back, or a credential file lost on power failure.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write atomically replaces path's contents with data: MkdirAll the parent
// directory (dirPerm), write to a same-directory temp file (perm), then
// rename over path. The temp file uses a fixed name (not
// os.CreateTemp's random suffix) since callers are expected to serialize
// writes to the same path themselves (e.g. a package-level sync.Mutex) -
// see each caller's own comment.
func Write(path string, data []byte, perm, dirPerm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	// OpenFile only applies perm when it CREATES the file - a stale .tmp a
	// previous crash left behind keeps its old mode. Chmod makes the file
	// renamed over path always end up with exactly perm (0600 for a
	// credential).
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	// Data before rename, rename before the directory entry: otherwise a
	// power loss can leave path pointing at an empty file.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
