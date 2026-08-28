package vhostpwa

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnv(t *testing.T) {
	got := parseEnv([]string{
		"ROUTER_VHOST_TRILIUM=note.example.com=trilium:8080",
		"ROUTER_VHOST_PWA_TRILIUM=Selene Notes|Selene",
		"ROUTER_VHOST_PWA_ICON_TRILIUM=/var/lib/code-docker-router/pwa/trilium.png",

		// A rewrite with a non-default manifest path.
		"ROUTER_VHOST_OTHER=other.example.com=other:80",
		"ROUTER_VHOST_PWA_OTHER=Other App|Other|/manifest.json",

		// No matching ROUTER_VHOST_ entry - nothing to fetch from.
		"ROUTER_VHOST_PWA_ORPHAN=Ghost|Ghost",
		// Missing the short name.
		"ROUTER_VHOST_NOSHORT=x.example.com=x:80",
		"ROUTER_VHOST_PWA_NOSHORT=OnlyName",
		// Switched off by emptying the value.
		"ROUTER_VHOST_OFF=y.example.com=y:80",
		"ROUTER_VHOST_PWA_OFF=",
	})

	if len(got) != 2 {
		t.Fatalf("expected 2 overrides, got %d: %+v", len(got), got)
	}

	tri, ok := got["trilium"]
	if !ok {
		t.Fatalf("no trilium override in %+v", got)
	}
	if tri.Name != "Selene Notes" || tri.ShortName != "Selene" {
		t.Errorf("name/short not parsed: %+v", tri)
	}
	if tri.ManifestPath != DefaultManifestPath {
		t.Errorf("ManifestPath = %q, want the default", tri.ManifestPath)
	}
	if tri.Upstream != "trilium:8080" {
		t.Errorf("Upstream = %q - it must come from the ROUTER_VHOST_ entry", tri.Upstream)
	}
	if tri.IconFile == "" {
		t.Errorf("icon not picked up: %+v", tri)
	}

	if got["other"].ManifestPath != "/manifest.json" {
		t.Errorf("explicit manifest path ignored: %+v", got["other"])
	}
	if got["other"].IconFile != "" {
		t.Errorf("other has no icon configured, got %q", got["other"].IconFile)
	}
}

// A 1x1 red PNG, so the dimensions in the manifest can be checked against a
// real file rather than a declared guess.
const onePixelPNG = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\xcf\xc0\x00\x00\x03\x01\x01\x00\x18\xdd\x8d\xb0\x00\x00\x00\x00IEND\xaeB`\x82"

func TestPatchMergesInsteadOfAuthoring(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manifest.webmanifest" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"name":"Trilium Notes","short_name":"Trilium","display":"standalone","scope":"/","start_url":"/","display_override":["window-controls-overlay"],"icons":[{"src":"icon.png","sizes":"512x512","type":"image/png"}]}`))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	iconPath := filepath.Join(dir, "icon.png")
	if err := os.WriteFile(iconPath, []byte(onePixelPNG), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := Patch(Override{
		Name:         "Selene Notes",
		ShortName:    "Selene",
		ManifestPath: DefaultManifestPath,
		IconFile:     iconPath,
		Upstream:     strings.TrimPrefix(upstream.URL, "http://"),
	})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["name"] != "Selene Notes" || m["short_name"] != "Selene" {
		t.Errorf("name/short_name not overridden: %v", m)
	}
	// The whole point of merging: fields we never heard of survive.
	if m["display"] != "standalone" || m["scope"] != "/" || m["start_url"] != "/" {
		t.Errorf("passthrough fields lost: %v", m)
	}
	if _, ok := m["display_override"]; !ok {
		t.Errorf("display_override dropped - a hand-authored manifest is exactly what this avoids")
	}

	icons, ok := m["icons"].([]any)
	if !ok || len(icons) != 1 {
		t.Fatalf("icons = %v", m["icons"])
	}
	icon := icons[0].(map[string]any)
	if icon["src"] != IconPath {
		t.Errorf("icon src = %v, want %s", icon["src"], IconPath)
	}
	// Read off the file, not declared.
	if icon["sizes"] != "1x1" {
		t.Errorf("icon sizes = %v, want the real PNG dimensions", icon["sizes"])
	}
}

func TestPatchWithoutIconLeavesIconsAlone(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"App","icons":[{"src":"icon.png","sizes":"512x512"}]}`))
	}))
	defer upstream.Close()

	out, err := Patch(Override{
		Name: "Renamed", ShortName: "R", ManifestPath: DefaultManifestPath,
		Upstream: strings.TrimPrefix(upstream.URL, "http://"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	icons := m["icons"].([]any)
	if icons[0].(map[string]any)["src"] != "icon.png" {
		t.Errorf("icons must be untouched when no icon is configured: %v", m["icons"])
	}
}

func TestPatchFailsLoudlyOnBadUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	if _, err := Patch(Override{
		Name: "x", ShortName: "x", ManifestPath: DefaultManifestPath,
		Upstream: strings.TrimPrefix(upstream.URL, "http://"),
	}); err == nil {
		t.Fatal("a non-200 upstream must be an error, so nginx falls back")
	}
}
