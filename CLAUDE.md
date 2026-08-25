# router-docker

Scoped guidance for anyone (human or agent) working in this repo. Read
`plan.md` first — it's kept short (current status + a linked TODO only).
This repo follows the same convention code-docker's own `webmanager/`
subtree uses: `CLAUDE.md` + `plan.md` at the repo root, feature-specific
design/history docs under `.claude/` (`.claude/functional-router-plan.md` —
the vision/every-decision doc, implemented now, see `plan.md` — plus
`.claude/router-dns-plan.md` and `.claude/router-nginx-hardening-plan.md`,
both also implemented and verified, see `plan.md`; `.claude/archive/` holds
done/superseded design docs, e.g. `tailscale-design.md`, the original
tailscale-in-code-docker design this container's own tailscale feature
superseded).

## What this is

The network-boundary container for
[code-docker](https://github.com/qwreey/code-docker) — see
`.claude/functional-router-plan.md` for the full vision and every design
decision. code-docker's own `.claude/backlog/egress-netgate-plan.md` (in
that repo, not here) covers the egress/DNAT design this container started
from (it was `code-docker-netgate`, living inside code-docker itself,
before being promoted first to a subtree and then to this standalone repo).
Don't re-derive decisions already recorded there.

Brought into code-docker as a git submodule at `router/`, with its own
`docker-compose.router.yml` that code-docker's own `docker-compose.yml`
includes by a fixed path — see code-docker's own `CLAUDE.md` for how the
two fit together (network topology, shared env vars like
`ROUTER_HOSTNAME`/`NETGATE_ENABLED`). This repo also builds and runs
standalone (`docker build .`, or `docker compose -f
docker-compose.router.yml` against an existing `code-docker-internal`/
`code-docker-external` network pair) for anyone using it outside
code-docker — confirmed via a clean `docker build .` from a fresh clone.
Carries its own `envmigrate` submodule (`envmigrate/`,
`vendor-envmigrate.sh`) rather than depending on anything outside this repo.

## Current state

The netgate→router migration described in `functional-router-plan.md` is
complete (see `plan.md`) — every feature area it envisioned lives here now.
See "Feature areas (detailed)" below for the full breakdown (netgate,
tailscale, Dev Proxy, App Routes, tinyauth, router-manager's API, DNS
management, this repo's own frontend, router-manager's own admin-API auth).

## Ground rules

- Follow code-docker's own `CLAUDE.md` override pattern (`<name>.default.*`
  + gitignored `<name>.override.*`, dispatched via a thin
  `script/<name>.sh`) for anything new added here — same idiom
  `config/netgate/` and `script/netgate-*.sh` already use.
- This container is meaningfully more trusted than code-docker (same
  framing code-docker's own dind-authz uses for dind) — its own
  packages/config shouldn't be reachable from inside code-docker at all,
  and anything added here should be held to that higher trust bar.
- Before running `docker compose build`/`up`/`restart` against a live
  container, confirm it's actually safe — someone else may be iterating on
  it.

## Feature areas (detailed)

User-facing docs: `docs/router.md`, `docs/egress-netgate.md`, `docs/dev-proxy.md`,
`docs/app-routes.md`, `docs/vnc.md`, `docs/tailscale.md` (now a short pointer into
`docs/router.md`).

Five feature areas. The first four each own supervisord programs (`config/supervisord.d/*.conf`,
git-tracked built-in program definitions — see `config/netgate/supervisord.default.conf`'s
own comment on the two `[include]` globs, one git-tracked for built-ins, one gitignored for
user overrides, same auto-include idiom as the main image's `config/supervisord.default.conf`);
the fifth (VNC) runs no process of its own at all — it's router-manager plus an App
Routes fragment, see its own bullet below:

- **netgate (egress lockdown)** — netinit-style routing enforcement (code-docker/dind side)
  plus router's own filtering (DNS-level content blocklist via dnsmasq, RFC1918/CIDR
  blocking, inbound port-forwarding). `code-docker-netinit` is a small sidecar built from its
  own self-contained subtree (`netinit/` — own Dockerfile/build context, see
  `netinit/CLAUDE.md`; same pattern as `router/` and `code-dind/`). It runs with
  `network_mode: service:code-docker` (shares code-docker's
  netns entirely — same interfaces/IP/routing table, not a separate IP) and
  `cap_add: [NET_ADMIN]`, a capability code-docker itself never gets. `netinit/script/netinit-entrypoint.sh`
  loops every 5s doing `ip route replace default via <router's resolved IP>`, defensively
  (never exits non-zero, tolerates `router` not resolving) — this is what keeps code-docker's
  default route pointed at router without code-docker ever being able to undo it.
  `code-docker-dind` needs no separate sidecar for the same mechanism — it's already
  `privileged: true`, so `code-dind/script/dind-entrypoint.sh` runs the identical loop itself
  (backgrounded before its final `exec`, wrapped in `tini` so dockerd-as-PID-1 doesn't
  accumulate zombies from the loop's repeated `ip`/`getent` forks). `script/entrypoint.sh`
  gates everything network-sensitive (starting with `user-init.sh`'s qwreey-fish curl)
  behind a bounded poll (60s timeout) for `ip route show default` to be non-empty — this is
  what code-docker waits on for netinit to have planted a route, without a compose-level
  `depends_on` cycle (route *reads* need no capability, only *writes* do). `router` is the
  only container attached to both `code-docker-internal` and `code-docker-external`,
  `cap_add: [NET_ADMIN]` only (no `privileged: true`).
  - `[program:netgate-firewall]` (`config/netgate/firewall.default.sh`) loops every
    30s reading `config/netgate/config.default.yaml` (override pattern) via `yq -r`
    and translating it into iptables: an ordered `outbound:` allow/block CIDR list
    (first-match-wins, specific exceptions before broad blocks — default blocks RFC1918 +
    link-local + loopback) and a `forwards:` port-forwarding list (default: host `80` →
    `code-docker:80`, generalized to any `code-docker-internal` hostname, not hardcoded). A
    forward's ACCEPT rule always lands before the CIDR blocks, since the target's own IP is
    itself in RFC1918 range. A stateful `ESTABLISHED,RELATED` ACCEPT rule comes first of all
    — without it, return traffic for an already-permitted connection (e.g. the port-80
    DNAT's reply) gets re-evaluated against the block rules and dropped, since Docker's own
    bridge subnets are themselves RFC1918 addresses. `net.ipv4.ip_forward=1` is set via this
    service's own `sysctls:` in docker-compose.yml, not a runtime `sysctl -w` — Docker keeps
    `/proc/sys` read-only for non-privileged containers regardless of `NET_ADMIN`, so a
    runtime write silently fails with permission denied. Same-subnet traffic (code-docker↔dind,
    code-docker↔router) never reaches this chain at all — connected-route traffic bypasses
    the gateway entirely, so no RFC1918 exception is needed for `code-docker-internal`'s own
    CIDR.
  - `[program:dns]` (`config/dns/dns.default.sh` +
    `config/dns/dnsmasq.default.conf`) is code-docker/dind's upstream DNS resolver —
    `code-docker-internal` being `internal: true` means Docker's own embedded DNS
    (`127.0.0.11`) refuses to forward queries externally, so both eventually point at router
    instead (see `.claude/router-dns-plan.md`) — code-docker itself does this
    indirectly now, through its own local `dns-local` resolver (`config/dns-local/`, see
    `.claude/backlog/dns-local-servfail-fix.md`) rather than router directly in
    `/etc/resolv.conf`; `code-docker-dind` still points its own `/etc/resolv.conf` at router
    the plain way. dnsmasq forwards upstream using router's own (working, non-internal) `/etc/resolv.conf`
    by default, or a fixed custom upstream list (e.g. `1.1.1.1`) if configured — see
    "DNS management" below. This also doubles as the content blocklist enforcement point:
    dnsmasq's `addn-hosts=` answers `0.0.0.0` for any domain in the baked-in StevenBlack/hosts
    file (no format conversion needed — dnsmasq reads hosts-format directly), now web-managed
    (multiple sources, not a single static file — see "DNS management" below) rather than a
    build-time-only `blocklist.default.hosts`/`blocklist.override.hosts` pair. This
    replaced an earlier squid-based intercept/SNI-block approach (`REDIRECT` on ports 80/443
    to squid, blocking by `dstdomain`/SNI) — removed because squid's `ssl_bump` anti-spoofing
    check false-positived on CDN-style domains with rotating IP pools (e.g.
    `registry-1.docker.io`), breaking `docker pull`. Block-only, no whitelist mode, and
    still explicitly best-effort/passive (adblock-like) — the hard boundary remains the
    RFC1918/CIDR FORWARD rules above.
  - `NETGATE_ENABLED` (env, default `true`) is a **behavioral** opt-out only — `false`
    makes the netinit/dind/router loops idle and skips entrypoint.sh's wait gate. It does
    **not** restore `code-docker-external`/`ports: - 80:80` on code-docker or dind —
    Compose can't conditionally attach a network or publish a port based on a runtime env
    var, so a full topology rollback is a deliberate manual edit to docker-compose.yml (same
    spirit as `DIND_TARGET=dind` to fully disable dind-authz) — see example-env's
    `NETGATE_ENABLED` comment for the exact steps. `profiles:` was considered for this
    opt-out and rejected: Compose profiles are opt-in by nature (a profiled service only
    starts when its profile is explicitly activated), which can't express "on by default
    even with zero `.env` file" — a hard requirement here per this repo's "works with no
    `.env` at all" philosophy.
- **tailscale** — `tailscaled`, `tailscale-forward`, and `tailscale-publish` run as three
  separate, single-responsibility supervisord programs (`config/tailscale/*.default.sh`),
  deliberately kept apart so e.g. editing `config.yaml` and restarting `tailscale-forward`
  never touches the `tailscaled` login session or `tailscale-publish`. Moved here from
  code-docker in full (daemon+login+forwards+publish, not partial) — code-docker itself has
  zero tailscale processes/packages now. `TAILSCALE_ENABLED`/`TAILSCALE_LOGIN_SERVER`/
  `TAILSCALE_HOSTNAME` (docker-compose env, same names as before the move) configure it.
  `TAILSCALE_LOGIN_SERVER` is also settable from the Tailscale tab's own "기본 설정"
  (`config.yaml`'s `login_server` field, alongside `forwards:`/`publish:` — see below) —
  the env var always wins when set (an infra-as-code pin, same priority
  `ROUTER_MANAGER_AUTH_PASSWORD_HASH`/`TINYAUTH_AUTH_USERS` already use), and the UI
  field renders read-only with a note in that case (`backend/internal/tailscale`'s
  `GlobalConfig.LoginServer`/`LoginServerPinned`/`EffectiveLoginServer`). An unset UI
  value behaves identically to today's unset-env default (tailscale.com's own SaaS) — the
  field is never prefilled. The Tailscale tab's "재인증" button (`tailscale up
  --force-reauth`, `POST /api/tailscale/login/start` with `{"forceReauth": true}`) issues a
  fresh login URL even while already logged in; switching to a genuinely different login
  server on an already-authenticated node still goes through the pre-existing "wipe
  `tailscale/state` and restart" procedure (`docs/router.md`) rather than "재인증" alone,
  since `tailscale up`'s own flag semantics around that combination aren't confirmed safe.
  Inbound: `tailscaled`'s netstack auto-forwards any tailnet connection to the same port on
  `127.0.0.1`, unconditionally, for any port with no `tailscale serve` rule (core
  `tailscaled` behavior) — since code-docker no longer runs tailscaled at all, this only
  matters for router's own ports now, not code-docker's. Outbound (forwards): `socat` piped
  through `tailscaled`'s local SOCKS5 proxy, listening on router's own `forward` alias on
  `code-docker-internal` (moved from code-docker's own now-removed dedicated
  `code-docker-forwards` network — forwards/publish sharing a port namespace was only ever
  a risk while both lived inside code-docker itself; now that forwards' socat and publish's
  `tailscale serve` both live on router, and publish proxies to a *different* container
  rather than binding a local port at all, that collision can't happen, so the extra network
  was dropped) so `forward:<port>` still resolves from inside code-docker, now pointing at
  router.
  `${ROUTER_VOLUME:-./data/router}/tailscale/config.yaml` (seeded from
  `config/tailscale/tailscale-config.default.yaml`) drives `forwards:`/`publish:` —
  MagicDNS names are deliberately never used as forward/publish targets (too dynamic,
  can even point at something outside the tailnet on self-hosted control servers), only
  tailscale hostnames/IPs. `publish:` entries name their own `target_host` (any
  `code-docker-internal` hostname/IP reachable from router — a plain compose service
  hostname works the same way code-docker's does, no alias dance needed — that was only
  ever about dodging code-docker's *own* tailscaled's auto-exposure, moot once tailscaled
  isn't there); omitting `target_host` defaults to `code-docker` for entries written
  before the field existed.
  router-manager (below) replaces the old status-polling shell script with a real read-only
  HTTP endpoint, and its own `/api/tailscale/forwards`/`/api/tailscale/publish`
  CRUD already persists+restarts the affected program in one call — editing
  `config.yaml` by hand and reloading via `docker compose exec
  code-docker-router supervisorctl restart ...` (see docs/router.md) is only
  needed if you bypass that API. `bin/forward-reload` (the old code-docker-side
  shortcut for printing that command) was removed since it couldn't actually
  reach router's supervisorctl socket from a different container and the API
  path above makes it unnecessary.
- **Dev Proxy** — an internal Caddy instance (`caddy-adapter` program,
  `config/caddy-adapter/caddy-adapter.default.sh`) exposing dev servers on wildcard
  subdomains, managed via router-manager's API (`backend/internal/devproxy`,
  `CADDY_ADAPTER_ENABLED`/`CADDY_ADAPTER_PORT` env, same names as before the move — also
  read by code-docker's nginx to build its `/exports/` proxy target). Moved here from
  code-docker in full, same reasoning as tailscale.
- **VNC** (2026-08-25, `.claude/archive/router-vnc-tab-plan-done.md` in code-docker's
  own repo) — a browser-embedded viewer for GUI containers attached to router
  (`backend/internal/vnc`, `frontend/src/components/Vnc/`, `GET`/`POST`/`PUT`/`DELETE
  /api/vnc/targets[/{name}]`, docs/vnc.md). Deliberately **not a new proxy mechanism**:
  router's Caddy is stock (HTTP/WS only, no layer4 plugin), so raw RFB can never ride App
  Routes/Dev Proxy — what gets proxied is the target's *web* VNC front end
  (noVNC+websockify, typically `:6080`, running in front of an unchanged wayvnc on the
  target's side), and App Routes is already the right carrier for that. So this package
  owns only what App Routes has no concept of (a display Label, and which `Backend`'s
  viewer URL shape to build) and drives an approutes fragment in lockstep with its own
  registry (`/var/lib/code-docker-router/vnc/targets.json`) — one action registers both.
  `List` re-reads approutes every call and reports drift (`RouteMissing`/`RouteDiverged`)
  rather than caching what it wrote, so a fragment deleted or repointed from the App Routes
  tab surfaces as a warning instead of a silently-404ing viewer; `Update` re-creates a
  missing route rather than failing, the tab's own self-heal. `Backend` ships with exactly
  one value (`novnc`) on purpose — the plan's decision was "noVNC와 Selkies를 나란히
  (타겟별 선택 가능하게) 지원", so a second backend should be one more `backendViewer`
  entry, not a schema change. Reaching a sibling project's container still needs that host
  in `ROUTER_EXTRA_ALLOWED_TARGET_HOSTS` (same `targetguard` allowlist App Routes uses —
  no separate one). The viewer's iframe `src` is origin-sensitive and can't just use
  `window.location.origin`: `/app/` is served only on the *shared* hostname's nginx block,
  never on a dedicated `ROUTER_MANAGER_HOSTS` domain (that block deliberately serves
  router-manager alone — putting user-registered app content on router-manager's own origin
  is exactly what that feature exists to prevent), so webmanager's `RouterFrame.tsx` passes
  its own origin in as `?origin=` and `frontend/src/components/Vnc/useViewerOrigin.ts`
  falls back to refusing to render a viewer, with an explanation, when the SPA is opened
  directly on a dedicated domain. Known limitation, verified live and inherited from the
  target side rather than introduced here: wayvnc's `VNC_PASSWORD` makes it demand VeNCrypt
  X509Plain, which current noVNC doesn't implement (`Unsupported security types (types:
  262)`) — gate the web path with the App-Routes-shared tinyauth `requireAuth` instead.
  `Target.ResizeMode` (2026-08-25, `remote`/`scale`/`off`, default `remote`, `""` on
  targets stored before it existed = the default) is the one target field that changes the
  viewer URL rather than the App Route: `remote` asks the target's own server to resize its
  desktop to the browser window (RFB `SetDesktopSize` — wayvnc has this on by default and
  its headless output follows live, verified against a real target), which is why it isn't
  forced viewer-wide — a server without that support just refuses, and noVNC then neither
  resizes nor scales, so those targets need `scale`. Sharper reason for per-target, found
  the hard way while shipping this: noVNC has **no lower bound** on the size it requests,
  so a viewer laid out at 0x0 (`display:none` iframe, no layout pass) asks for a 0x0
  desktop — wayvnc forwards that as a wlr-output-management custom mode, wlroots rejects
  width/height ≤ 0 as a *protocol error*, and libwayland makes that fatal, so wayvnc dies.
  Against a target that shuts itself down when its VNC server dies and a client that
  auto-reconnects, that's a restart loop (observed live against roblox-studio-docker,
  which fixed its own side by patching a 1x1 floor into its vendored noVNC). The viewer's 전체화면 button
  **delegates to noVNC's own fullscreen button** when the iframe is same-origin, rather
  than fullscreening the iframe from outside: the outside path never sets
  `document.fullscreenElement` *inside* the frame, so noVNC's own button stayed stale and
  took two presses to escape. The cross-origin fallback fullscreens the whole viewer card
  (not the bare iframe) so the header's own exit button stays on screen.
- **tinyauth** — router's own forward-auth, run as a plain supervisord program inside
  router itself (`config/tinyauth/tinyauth.default.sh`), not a separate compose
  service — `Dockerfile` multi-stage-extracts the prebuilt binary straight from
  `ghcr.io/tinyauthapp/tinyauth` (its own Dockerfile requires a mandatory pnpm/Vue
  frontend build ahead of its Go build, so it isn't rebuilt from source like dind-authz,
  but the finished binary itself needs no such step and copies over cleanly). Sleeps
  instead of starting when `TINYAUTH_APPURL` is unset (tinyauth refuses to boot without a
  real URL) — same opt-out idiom as `CADDY_ADAPTER_ENABLED`/`TAILSCALE_ENABLED`, and what
  keeps an unconfigured instance from crash-looping. Protects individual Dev Proxy routes
  that opt into "require auth" (Caddy `forward_auth` → tinyauth's `/api/auth/caddy` on
  `127.0.0.1:3000`) — a separate, lighter tool from webmanager's own `internal/authgate`,
  which stays exactly as-is, scoped only to webmanager's own Terminal/File Manager/Logs.
  `TINYAUTH_AUTH_USERS` (docker-compose env) is empty by default — no one can log in until
  set (`docker run --rm ghcr.io/tinyauthapp/tinyauth:v5 user create --username <u>
  --password <p> --docker` generates the value). The recommended path is now per-user
  add/delete via router-manager's own API/UI (`backend/internal/tinyauthusers`,
  a "설정" tab in router's own SPA — see "router-manager" below) instead of hand-editing
  that one env var — tinyauth itself only reads it at process start, so every add/delete
  restarts the `tinyauth` supervisord program via the same `restartSupervisorProgram`
  helper tailscale forwards/publish already use. `TINYAUTH_AUTH_USERS` still wins when
  actually set (an infra-as-code pin, same priority as `ROUTER_MANAGER_AUTH_PASSWORD_HASH`
  vs its own file-backed store) — the UI shows a read-only notice instead of an edit form
  in that case.

router-manager is this container's own Go backend (`backend/`, mirrors code-docker's own
webmanager backend pattern) — this container's own nginx (not code-docker's) terminates host:80 directly and
proxies to it over a unix socket (`/run/router-manager.sock`) under one unified `/router/`
location (`config/nginx/nginx.default.conf`, also serving the built SPA — see
"router's own frontend" below); router-manager itself opens no TCP port by default
(`ROUTER_MANAGER_ADDR` is an opt-in TCP escape hatch for local dev outside the container).
The old per-feature code-docker-nginx locations (`/tailscale/`, `/dev-proxy/`,
`/router-auth/`) are gone — code-docker isn't even attached to `code-docker-external`
anymore, so it was never a legitimate proxy point for this. Routes router-manager serves:
full tailscale CRUD (`GET`/`PUT
/api/tailscale/config`, `GET`/`POST`/`PUT`/`DELETE /api/tailscale/forwards[/{name}]`,
same for `/publish`, `GET /api/tailscale/status`, `POST /api/tailscale/login/
{start,cancel}`, plus the original read-only `GET /api/tailscale/state`
— `{backendState, authUrl}`, same shape the old status-polling script wrote —
code-server's sign-in banner, `config/code/code-patch/tailscale-notify.default.js`,
polls this now instead of a static file), the Dev Proxy expose CRUD, the App Routes expose
CRUD (`backend/internal/approutes`, sharing self-SSRF target validation with Dev
Proxy via `internal/targetguard`), netgate's outbound/forwards/bandwidth-shaping CRUD
("Net 관리" tab, `backend/internal/netgate` — bandwidth is `GET`/`PUT
/api/netgate/bandwidth`, applied via a `tc` HTB tree on the default interface by its own
`netgate-shaping` supervisord program, see `config/netgate/shaping.default.sh` and
docs/egress-netgate.md's "대역폭 제한" section), tinyauth user CRUD (`backend/internal/
tinyauthusers`, "tinyauth" tab), DNS management (`backend/internal/dns`,
`GET /api/dns/blocklist-sources` + `POST`/`PUT`/`DELETE` for custom sources +
`GET`/`POST /api/dns/blocklist-sources/builtin/{status,pull,ignore}` for the
hash-tracked builtin source, `GET`/`PUT /api/dns/custom-hosts`,
`GET`/`PUT /api/dns/resolver` — see "DNS management" below), and
`POST /api/auth/unlock` + `GET /api/auth/status` for
router-manager's own admin-API password gate (see below). `/exports/` (actual
end-user traffic to an exposed dev server) and `/app/` (App Routes end-user traffic) are
both separate nginx locations from `/router/` (the admin API + SPA) — don't confuse them.

**DNS management** (2026-08-08, `.claude/dns-blocklist-management-plan.md`) —
DNS content blocklist and resolver override, previously pure build-time
default/override files with no runtime API, are now web-managed like
tailscale/Dev Proxy. Three pieces, all under `/var/lib/code-docker-router/dns/`:
(1) blocklist sources, one hosts-format file per source under
`dns/blocklist-sources/` — `builtin.hosts` is seeded from
`blocklist.default.hosts` **only** (deliberately not `.override.hosts` —
that file has always been an unconditional, purely additive extra
`--addn-hosts=` flag layered on top, not a replacement for the default, and
folding it into this seed step would have silently reversed that for
anyone already relying on it; it keeps working exactly as before,
independent of everything below) using `config/code/code-patch.default.sh`'s
own hash-tracking algorithm (`dns.default.sh`'s own `seed_builtin_blocklist`,
on every `dns` program start): missing → copy; shipped-content unchanged →
no-op; shipped-content changed and the live copy still matches what was
last seeded → silently re-copy (safe, no customization exists yet to lose);
shipped-content changed and the live copy has diverged (edited via the web
UI) → leave it alone, and `GET /api/dns/blocklist-sources/builtin/status`
reports `updateAvailable` with an added/removed host diff sample plus
pull/ignore actions — this is the one behavioral difference from
code-patch, which just leaves a diverged file alone forever with no
follow-up. Any number of additional custom sources can be added via the web
UI (`POST /api/dns/blocklist-sources`) — dnsmasq accepts `--addn-hosts=`
repeated, so `dns.default.sh` just globs every file in the directory.
(2) `dns/custom-hosts.yaml`/`.hosts` — MagicDNS-style custom hostname→real-IP
entries (`GET`/`PUT /api/dns/custom-hosts`, whole-list replace), loaded
*before* every blocklist source in `dns.default.sh`'s `--addn-hosts=`
sequence (a fixed precedence decision, not user-configurable — see the plan
doc for why dnsmasq's own multi-file-hosts precedence isn't reliable enough
to fully resolve a host appearing in both, and how `duplicateHosts` in
`GET /api/dns/blocklist-sources` surfaces that ambiguity as a warning
instead). (3) `dns/config.yaml`'s `resolver: {mode, servers}` — `auto`
(default, unchanged: dnsmasq reads this container's own `/etc/resolv.conf`)
or `custom` (a fixed upstream list, e.g. `1.1.1.1` — `--no-resolv --server=`
flags, confirmed feasible in userspace). `dnsmasq.default.conf`'s own static
`addn-hosts=` line was removed since it can't reflect any of this at
runtime. Applying the same reconcile pattern to netgate's firewall config
was considered and explicitly deferred — see the plan doc's own section on
why that needs netgate to first adopt the same "seeded live copy" model
before hash-reconcile means anything coherent there (netgate today reads
`config.default.yaml`/`.override.yaml` directly, with no seeded copy to
diverge from).

router's own frontend (`frontend/` — its own npm project, not a code-docker workspace
member anymore, see below) owns the actual page components (Dev Proxy,
App Routes, Tailscale, DNS, Net 관리, tinyauth). Since a 2026-08-08 decoupling pass (see
`.claude/net-auth-expansion-plan.md`), code-docker's
webmanager no longer imports any of these components directly — it `<iframe>`-embeds this
container's own `/router/` page instead (`webmanager/frontend/src/components/RouterEmbed/RouterFrame.tsx`
in the code-docker repo), and `webmanager/frontend/package.json` there carries zero
`@code-docker/router-frontend` dependency at all anymore (that package itself is gone -
`frontend/` was never actually published anywhere, just formerly an npm workspace member of
code-docker's root `package.json`, since dropped now that this is a standalone repo). The
generic UI primitives webmanager used to borrow from it (`ErrorBanner`/`Sheet`/`Skeleton`)
were hand-duplicated into webmanager's own tree as part of the same pass. `frontend/`'s own
`App.tsx` (a plain tab switcher, no react-router) is also
built into a real SPA now — `Dockerfile` has its own Node build stage (using
`frontend/package-lock.json`, self-contained since this repo has no workspace root to reach
into) and
`backend/static.go` (ported from code-docker's `webmanager/backend/static.go`) serves it directly
at `/router/`, replacing the old password-only `handlers_ui.go` page — so App
Routes/Dev Proxy/Tailscale/tinyauth users can all be managed without webmanager at all, only
router-manager's own API. First-run password setup/change now lives in this SPA too
(`RouterAuthPanel`, a React port of the old inline-JS page), under a "설정" tab alongside a
new `TinyauthUsers` panel (see below). `frontend/vite.config.ts`'s build `base` is
`'./'` (relative), not an absolute prefix like webmanager's own `'/manager/'` — this SPA is
served from two different depths depending on deployment (the shared hostname's `/router/`
path, or the root of a dedicated `ROUTER_MANAGER_HOSTS` domain, see below), and only a
relative base resolves correctly under both as long as the page itself is always linked with
a trailing slash. Confirmed live that an absolute `/` base 404s every asset under `/router/`,
since the browser resolves a root-absolute `src` against the origin root, bypassing the
`/router/` prefix entirely.

router-manager's own admin-API auth (`backend/internal/authgate`) is opt-in via
`ROUTER_MANAGER_AUTH_PASSWORD_HASH` and gates every *mutating* route above (tailscale
config/forwards/publish/login writes, dev-proxy expose writes) — reads (state, config,
list, status) stay open. The recommended path is setting a password in-app at
`/router/` instead of via env var, though — see docs/router.md's "router-manager 자체
인증" for the file-backed store (`ROUTER_VOLUME`), setup/change UI, and forgot-password
recovery; the env var remains as an infra-as-code pin that always wins over the
in-app-set one when present. A separate gate/cookie from webmanager's own
`internal/authgate` below (different process, different secret) — `router-manager
--hash-password` generates the argon2id hash. `RouterUnlockModalHost` (mounted in
webmanager's `App.tsx` next to its own `UnlockModalHost`) pops on any 401 from a gated
router-manager route, same "prompt → retry once" pattern webmanager's own gate uses. See
`plan.md` for the design history (this closed out the item that was previously
tracked there as "보류/미정").

The unlock cookie (`router_manager_unlock`) is host-only with no Domain attribute, and
`/router/` is reachable on the *shared* hostname by default (same origin as code-server/
webmanager/every `/exports/` and `/app/` target) — so a compromise anywhere on that shared
origin (XSS, a poisoned agent writing to the page) can ride the cookie into router-manager's
API via a same-origin `fetch()`; HttpOnly/SameSite=Strict only stop cross-origin/JS-read
access, not same-origin script. Router's own nginx strips the cookie from the proxied
`Cookie` header on `/exports/` and `/app/` (`router_manager_cookie_stripped` map in
`config/nginx/nginx.default.conf`) so an untrusted Dev Proxy/App Routes target can't
read it directly — but that doesn't close the same-origin-script vector. `ROUTER_MANAGER_HOSTS`
(`example-env.router`, comma-separated, default empty) is the actual fix: it adds a
second `server{}` block (env-only/restart-required, same trust tier as `ALLOWED_HOSTS`/
`ALLOWED_EXPORT_HOSTS` — never made in-app-editable) that serves router-manager's SPA+API
standalone on a dedicated hostname via nginx `server_name` matching, so its cookie is scoped
to that origin alone. `frontend`'s `RouterAuthPanel` (which shows the configured
`trustedHosts` read-only) and `OriginWarningBanner` warn when accessed
over localhost or over the shared path despite a dedicated domain being configured — see
docs/router.md's "보안: 공유 origin과 전용 도메인" section.

`ROUTER_MANAGER_HOSTS` also changes how webmanager itself embeds the Dev
Proxy/App Routes/Tailscale/DNS/Net 관리/tinyauth/설정 tabs — see "webmanager" below and
`webmanager/frontend/src/components/RouterEmbed/RouterFrame.tsx` — switching the
`<iframe>`'s `src` from the same-origin `/router/` path (the default, when
`ROUTER_MANAGER_HOSTS` is unset) to the dedicated cross-origin domain instead, which is what
actually closes the ambient-cookie gap for those tabs: a same-origin iframe still shares a
browsing context whose DOM anything compromising webmanager itself could reach, while a true
cross-origin iframe has none. Both cases are an `<iframe>` — there is no same-origin
direct-render fallback (that existed before the 2026-08-08 decoupling above, since removed).
`frontend/src/embedTheme.ts`'s `?theme=`/`postMessage` handling
(and the matching `[data-theme]` CSS blocks in `frontend/src/index.css`, mirroring
webmanager's own `theme.ts` idiom) keep the embedded iframe's light/dark choice in sync with
webmanager's, since a cross-origin iframe can't read the parent's `data-theme` attribute
directly the way a same-origin embed implicitly could.

