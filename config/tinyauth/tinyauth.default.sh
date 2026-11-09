#!/bin/bash
set -e

# router's own forward-auth for individual Dev Proxy exposes that opt into
# RequireAuth (see router/backend/internal/devproxy's TinyauthTarget and
# docs/dev-proxy.md#인증). Runs the official tinyauth binary directly
# (extracted from ghcr.io/tinyauthapp/tinyauth in router/Dockerfile) as a
# plain router supervisord program instead of its own compose service -
# tinyauth's own Dockerfile requires a mandatory pnpm frontend build ahead
# of its Go build, which doesn't fit the dind-authz from-source pattern,
# but the prebuilt binary itself needs no such step and drops in here just
# fine.
#
# TINYAUTH_APPURL is the URL tinyauth builds its login-page redirects from;
# TINYAUTH_HOSTS is the hostname router's own nginx actually serves that
# login page on (config/nginx/nginx-service.default.sh). In every real
# deployment those are the same hostname typed twice, so derive one from
# the other rather than making the user keep two values in sync by hand -
# a mismatch between them fails *silently*, sending the browser to a host
# that serves something other than tinyauth.
#
# https because router itself always terminates plain HTTP behind someone
# else's TLS-terminating reverse proxy, so the scheme the browser uses is
# effectively always https; set TINYAUTH_APPURL explicitly to override
# (a plain-http deployment, or an external URL that differs from the
# internal server_name).
if [ -z "${TINYAUTH_APPURL:-}" ] && [ -n "${TINYAUTH_HOSTS:-}" ]; then
    tinyauth_first_host="$(echo "$TINYAUTH_HOSTS" | cut -d',' -f1 | xargs)"
    if [ -n "$tinyauth_first_host" ]; then
        TINYAUTH_APPURL="https://$tinyauth_first_host"
        export TINYAUTH_APPURL
        echo "tinyauth: TINYAUTH_APPURL not set - derived $TINYAUTH_APPURL from TINYAUTH_HOSTS"
    fi
fi

# tinyauth itself refuses to start (exits immediately) without
# TINYAUTH_APPURL set to a real URL - sleep instead of crash-looping when
# it's unset, same opt-out idiom as CADDY_ADAPTER_ENABLED/TAILSCALE_ENABLED.
if [ -z "${TINYAUTH_APPURL:-}" ]; then
    echo "tinyauth: neither TINYAUTH_HOSTS nor TINYAUTH_APPURL is set - see example-env.router's tinyauth section"
    exec sleep infinity
fi

# tinyauth hardcodes its persistent state (sqlite db, sessions, ...) under
# /data (the upstream image declares it as a VOLUME) - point that at
# router's own persistent volume instead of the image's ephemeral layer,
# same reasoning as tailscale's state under
# /var/lib/code-docker-router/tailscale/state.
# 700: tinyauth keeps its session store here.
mkdir -p /var/lib/code-docker-router/tinyauth
chmod 700 /var/lib/code-docker-router/tinyauth
[ -e /data ] || ln -s /var/lib/code-docker-router/tinyauth /data

# router-manager's own per-user credential CRUD (see
# internal/tinyauthusers) writes TINYAUTH_AUTH_USERS to this file whenever a
# user is added/removed, then restarts this program - only sourced when the
# real env var isn't already pinning the value, same priority as
# ROUTER_MANAGER_AUTH_PASSWORD_HASH vs its own file-backed store.
if [ -z "${TINYAUTH_AUTH_USERS:-}" ] && [ -e /var/lib/code-docker-router/tinyauth-users/env ]; then
    . /var/lib/code-docker-router/tinyauth-users/env
    export TINYAUTH_AUTH_USERS
fi

# tinyauth exits on the spot ("failed to bootstrap app: no authentication
# providers configured") when nothing at all can authenticate anyone - which
# is exactly the state router-manager leaves behind the moment the last user
# is deleted. Idle instead of crash-looping, same opt-out idiom as the
# TINYAUTH_APPURL check above: the delete request that caused it would
# otherwise come back as `supervisor fault 50: SPAWN_ERROR: tinyauth` for a
# change that saved perfectly well. Adding a user restarts this program
# anyway, so nothing else has to notice. Only the local-user path is ours to
# reason about - any other provider being configured means tinyauth has its
# own opinion about whether it can start, so stay out of the way.
if [ -z "${TINYAUTH_AUTH_USERS:-}" ] \
    && [ -z "${TINYAUTH_AUTH_USERSFILE:-}" ] \
    && [ -z "${TINYAUTH_LDAP_ADDRESS:-}" ] \
    && [ "${TINYAUTH_TAILSCALE_ENABLED:-false}" != "true" ] \
    && ! env | grep -q '^TINYAUTH_OAUTH_PROVIDERS_'; then
    echo "tinyauth: no users configured (and no OAuth/LDAP/Tailscale provider) - idling until one is added from router's own 설정 tab, or via TINYAUTH_AUTH_USERS"
    exec sleep infinity
fi

# Two settings forced here, after .env.router has been loaded, so it can't undo
# them; `tinyauth config` then shows the configuration tinyauth will really
# run with, checked before starting it. Exported rather than passed as flags:
# tinyauth v5 ignores every TINYAUTH_* variable as soon as any flag is given,
# APPURL and AUTH_USERS included.
# - server.address 127.0.0.1: nginx, Caddy's forward_auth and App Routes all
#   reach tinyauth at 127.0.0.1:3000, and tinyauth's own default is 0.0.0.0,
#   which would put its login and forward-auth endpoints on every network
#   router is a gateway for (code-docker-internal, every sibling's isolated
#   network) without nginx in front. netgate-entrypoint.sh's
#   ROUTER_LOOPBACK_PORTS guard is the second layer.
# - analytics.enabled false: on by default upstream, it periodically reports
#   version information to tinyauth's servers from router, which has egress.
export TINYAUTH_SERVER_ADDRESS=127.0.0.1
export TINYAUTH_ANALYTICS_ENABLED=false
effective=$(/usr/local/bin/tinyauth config 2>&1 | sed 's/\x1b\[[0-9;]*m//g')
setting() {
    printf '%s\n' "$effective" | awk -v sec="$1:" -v key="$2:" '$0 == sec { s = 1; next } /^[^ ]/ { s = 0 } s && $1 == key { print $2; exit }'
}
address=$(setting server address)
# A false boolean is left out of the dump, so "" means off too.
analytics=$(setting analytics enabled)
if [ "$address" != 127.0.0.1 ] || [ "${analytics:-false}" != false ]; then
    echo >&2 "tinyauth: would run with server.address '$address' and analytics.enabled '$analytics', want 127.0.0.1 and false - not starting"
    exit 1
fi
exec /usr/local/bin/tinyauth
