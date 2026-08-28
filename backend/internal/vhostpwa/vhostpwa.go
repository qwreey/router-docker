// Package vhostpwa rewrites the PWA manifest of an app published through a
// vhost (ROUTER_VHOST_*, see config/nginx/nginx-service.default.sh), so that a
// second instance of an app the user already has installed is installable and
// tellable apart from the first.
//
// The concrete case: someone running their own Trilium at home attaches a
// second, project-scoped one here. Both serve the same manifest — "Trilium
// Notes", the same icon — so two installed PWAs are indistinguishable on the
// home screen, and the app has no setting for it (its manifest is a static
// file in the client build). Only the hop in front of it can fix that, and
// router is the only hop.
//
// It merges rather than authors: the upstream manifest is fetched and only the
// declared keys are replaced, so anything the app carries that we don't know
// about (display_override, share_target, protocol_handlers, ...) survives
// instead of being silently dropped by a hand-maintained copy. That is the
// same reasoning — and the same failure handling, a bare 502 that nginx's
// error_page turns into a fallback to the app's own manifest — as webmanager's
// internal/manifestpatch, which does this for code-server.
package vhostpwa

import (
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// EnvPrefix declares the override: "<name>|<short name>[|<manifest path>]".
	EnvPrefix = "ROUTER_VHOST_PWA_"
	// IconEnvPrefix optionally points at a PNG inside the router container
	// (normally under ROUTER_VOLUME, i.e. /var/lib/code-docker-router/...).
	IconEnvPrefix = "ROUTER_VHOST_PWA_ICON_"
	// VhostEnvPrefix is where the upstream to fetch from comes from — the same
	// entry that publishes the hostname in the first place, so the upstream is
	// never named twice and can never disagree with what nginx proxies to.
	VhostEnvPrefix = "ROUTER_VHOST_"

	// DefaultManifestPath is the standard location. code-server is the odd one
	// out with /manifest.json, hence the optional third field.
	DefaultManifestPath = "/manifest.webmanifest"

	// IconPath is where the replacement icon is published on the vhost, by the
	// nginx block that the same env vars generate. Fixed rather than derived
	// from the app, so the path an outer forward-auth has to leave public is
	// the same for every app (a PWA's icon must be fetchable with no cookies
	// at all — Android's WebAPK is built by Google's servers, not the phone).
	IconPath = "/_pwa-icon.png"
)

// Override is one parsed ROUTER_VHOST_PWA_<NAME> entry.
type Override struct {
	Key          string // the <NAME> part, as it appears in the env var
	Name         string
	ShortName    string
	ManifestPath string
	IconFile     string // empty when no icon was configured
	Upstream     string // host:port, from the matching ROUTER_VHOST_<NAME>
}

var (
	once      sync.Once
	overrides map[string]Override
)

// keyRe matches the lowercased env-var suffix, which is what the URL path
// carries. Same shape as a vhost name elsewhere in this repo.
var keyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Lookup finds the override for a vhost by its lowercased env-var suffix.
func Lookup(key string) (Override, bool) {
	once.Do(func() { overrides = parseEnv(os.Environ()) })
	o, ok := overrides[key]
	return o, ok
}

func parseEnv(environ []string) map[string]Override {
	values := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			values[k] = v
		}
	}

	out := map[string]Override{}
	for k, v := range values {
		if !strings.HasPrefix(k, EnvPrefix) || strings.HasPrefix(k, IconEnvPrefix) {
			continue
		}
		suffix := strings.TrimPrefix(k, EnvPrefix)
		key := strings.ToLower(strings.ReplaceAll(suffix, "_", "-"))
		if !keyRe.MatchString(key) || strings.TrimSpace(v) == "" {
			continue
		}
		parts := strings.Split(v, "|")
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		short := strings.TrimSpace(parts[1])
		manifestPath := DefaultManifestPath
		if len(parts) > 2 && strings.TrimSpace(parts[2]) != "" {
			manifestPath = strings.TrimSpace(parts[2])
		}
		if name == "" || short == "" || !strings.HasPrefix(manifestPath, "/") {
			continue
		}
		upstream, ok := upstreamOf(values[VhostEnvPrefix+suffix])
		if !ok {
			continue
		}
		out[key] = Override{
			Key:          suffix,
			Name:         name,
			ShortName:    short,
			ManifestPath: manifestPath,
			IconFile:     strings.TrimSpace(values[IconEnvPrefix+suffix]),
			Upstream:     upstream,
		}
	}
	return out
}

// upstreamOf pulls the "<upstream>" half out of a ROUTER_VHOST_<NAME> value.
// The shell that generates nginx's config validates the same string; this is
// only reading it back, so a value that fails here simply yields no override
// and the app's own manifest is served untouched.
func upstreamOf(vhostValue string) (string, bool) {
	_, upstream, ok := strings.Cut(vhostValue, "=")
	if !ok {
		return "", false
	}
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return "", false
	}
	return upstream, true
}

var httpClient = &http.Client{Timeout: 5 * time.Second}

// Patch fetches the app's own manifest and returns it with the declared keys
// replaced. Any failure is an error and nothing else: the caller answers a
// bare 502 so nginx falls back to the unmodified manifest, which still
// installs — just under the app's own name.
func Patch(o Override) ([]byte, error) {
	url := "http://" + o.Upstream + o.ManifestPath
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("vhostpwa: fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vhostpwa: upstream %s returned status %d", url, resp.StatusCode)
	}

	var manifest map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("vhostpwa: parse manifest from %s: %w", url, err)
	}

	manifest["name"] = o.Name
	manifest["short_name"] = o.ShortName

	if o.IconFile != "" {
		icon, err := iconEntry(o.IconFile)
		if err != nil {
			// Not fatal: a bad icon path should cost the icon, not the whole
			// manifest — the renamed app is still the point.
			return nil, err
		}
		manifest["icons"] = []any{icon}
	}

	return json.Marshal(manifest)
}

// iconEntry reads the PNG's real dimensions rather than declaring a size and
// hoping. A manifest whose sizes disagree with the file is the kind of thing
// browsers reject quietly, which is the failure mode this whole package
// exists to avoid.
func iconEntry(path string) (map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("vhostpwa: open icon %s: %w", path, err)
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return nil, fmt.Errorf("vhostpwa: decode icon %s: %w", path, err)
	}
	if format != "png" {
		return nil, fmt.Errorf("vhostpwa: icon %s is %s, want png", path, format)
	}
	return map[string]any{
		"src":   IconPath,
		"sizes": fmt.Sprintf("%dx%d", cfg.Width, cfg.Height),
		"type":  "image/png",
	}, nil
}
