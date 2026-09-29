package dns

import (
	"strings"
	"testing"
)

func TestDedupeFoldsCase(t *testing.T) {
	got := dedupe(lowerHosts([]string{"Example.com", "example.com", "EXAMPLE.COM", "other.org"}))
	if len(got) != 2 || got[0] != "example.com" || got[1] != "other.org" {
		t.Fatalf("got %v, want [example.com other.org]", got)
	}
}

// Rejected before anything is written, so this never touches the real
// (constant) store path.
func TestSetCustomHostsRejectsCaseOnlyDuplicate(t *testing.T) {
	in := []HostEntry{{Host: "Example.com", IP: "10.0.0.1"}, {Host: "example.com", IP: "10.0.0.2"}}
	err := SetCustomHosts(in)
	if err == nil || !strings.Contains(err.Error(), "duplicate host") {
		t.Fatalf("err = %v, want a duplicate host error", err)
	}
	if in[0].Host != "Example.com" {
		t.Fatalf("caller's slice was modified: %q", in[0].Host)
	}
}
