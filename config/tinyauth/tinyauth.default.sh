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
mkdir -p /var/lib/code-docker-router/tinyauth
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

exec /usr/local/bin/tinyauth
