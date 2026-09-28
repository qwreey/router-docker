# router-docker

The network-boundary container for [code-docker](https://github.com/qwreey/code-docker) —
egress lockdown (DNS-level content blocklist, RFC1918/CIDR filtering), tailscale
(forwards/publish), a Dev Proxy + App Routes reverse proxy, tinyauth forward-auth, a
browser VNC viewer (noVNC served by router itself, bridging the target's RFB port), and a
router-manager admin API/SPA (`/router/`) covering all of it.

Consumed by code-docker as a remote Compose include of this repo's
`docker-compose.router.yml`, pinned to a release tag — see
code-docker's own `CLAUDE.md` for how the two fit together (network topology, shared env
vars like `ROUTER_HOSTNAME`/`NETGATE_ENABLED`). This repo also builds and runs standalone
(`docker build .` / `docker compose -f docker-compose.router.yml` against an existing
`code-docker-internal`/`code-docker-external` network pair) for anyone using it outside
code-docker.

See `CLAUDE.md` for architecture, `plan.md` for current status, and `.claude/` for
feature-specific design docs.
