#!/bin/bash
set -e

nginx_config=/etc/router/nginx.default.conf
if [ -e /etc/router/nginx.override.conf ]; then
    nginx_config=/etc/router/nginx.override.conf
fi

# Same NGINX_LOG_LEVEL/ALLOWED_HOSTS/NGINX_BLOCK_LOOPBACK/TRUSTED_PROXIES
# handling as the main image's config/nginx-service.default.sh - these env
# vars are now shared across both code-docker's and router's environment:
# blocks (docker-compose.yml), since router is the actual internet-facing
# hop now and benefits from the same checks at its own layer too.
case "${NGINX_LOG_LEVEL:-errors}" in
    all)
        export NGINX_ACCESS_LOG_IF=""
        ;;
    *)
        export NGINX_ACCESS_LOG_IF=" if=\$loggable"
        ;;
esac

if [ -n "${ALLOWED_HOSTS:-}" ]; then
    map_body="default 0;"
    IFS=',' read -ra allowed_hosts <<< "$ALLOWED_HOSTS"
    for host in "${allowed_hosts[@]}"; do
        host="$(echo "$host" | xargs)"
        [ -n "$host" ] && map_body="$map_body
    \"$host\" 1;"
    done
else
    map_body="default 1;"
fi
export NGINX_ALLOWED_HOSTS_MAP="$map_body"

# ALLOWED_EXPORT_HOSTS moved here from code-docker's own nginx along with
# the /exports/ location itself (see router/.claude/router-nginx-hardening-plan.md).
if [ -n "${ALLOWED_EXPORT_HOSTS:-}" ]; then
    export_map_body="default 0;"
    IFS=',' read -ra allowed_export_hosts <<< "$ALLOWED_EXPORT_HOSTS"
    for host in "${allowed_export_hosts[@]}"; do
        host="$(echo "$host" | xargs)"
        [ -n "$host" ] && export_map_body="$export_map_body
    \"$host\" 1;"
    done
else
    export_map_body="default 1;"
fi
export NGINX_ALLOWED_EXPORT_HOSTS_MAP="$export_map_body"

case "${NGINX_BLOCK_LOOPBACK:-true}" in
    false)
        export NGINX_LOOPBACK_BLOCK_MAP="default 0;"
        ;;
    *)
        export NGINX_LOOPBACK_BLOCK_MAP="127.0.0.1 1;
    default 0;"
        ;;
esac

directives=""
if [ -n "${TRUSTED_PROXIES:-}" ]; then
    IFS=',' read -ra trusted_proxies <<< "$TRUSTED_PROXIES"
    for proxy in "${trusted_proxies[@]}"; do
        proxy="$(echo "$proxy" | xargs)"
        [ -n "$proxy" ] && directives="$directives
    set_real_ip_from $proxy;"
    done
fi
export NGINX_TRUSTED_PROXIES_DIRECTIVES="$directives"

# detect_internal_subnet - code-docker-internal has no pinned subnet in
# docker-compose.yml (Docker auto-assigns it, so multiple PREFIX instances
# on one host never fight over the same hardcoded CIDR). router sits on
# both code-docker-internal and code-docker-external, so it can find
# code-docker-internal's live CIDR itself: the interface NOT carrying the
# default route is always code-docker-external (the only network with a
# real gateway - code-docker-internal is `internal: true`), so the *other*
# connected/kernel-scope route belongs to code-docker-internal. This is the
# same "no default route = internal-side interface" heuristic
# dind-entrypoint.sh already uses to find its own IP. Only reliable while
# router has exactly these two networks - if a third one is ever added,
# fall back to setting ROUTER_INTERNAL_SUBNET explicitly.
detect_internal_subnet() {
    default_dev="$(ip -4 route show default 2>/dev/null \
        | awk '{for (i=1;i<=NF;i++) if ($i=="dev") { print $(i+1); exit }}')"
    [ -n "$default_dev" ] || return 1
    ip -4 route show scope link 2>/dev/null \
        | awk -v skip="$default_dev" '
            {
                dev=""
                for (i=1;i<=NF;i++) if ($i=="dev") dev=$(i+1)
                if (dev != "" && dev != skip) { print $1; exit }
            }'
}

# ROUTER_NGINX_DENY_INTERNAL_EXPORTS (default "true") - denies
# code-docker-internal source addresses from reaching /exports/ directly,
# using ROUTER_INTERNAL_SUBNET if set, else the CIDR detect_internal_subnet
# finds live above. "false" disables the check entirely (empty directive =
# nginx's own default, allow all) - the escape hatch for a topology where a
# trusted proxy (e.g. a user's own Caddy) legitimately reaches router *from
# inside* code-docker-internal instead of from outside.
case "${ROUTER_NGINX_DENY_INTERNAL_EXPORTS:-true}" in
    false)
        export NGINX_DENY_INTERNAL_EXPORTS_DIRECTIVE=""
        ;;
    *)
        internal_subnet="${ROUTER_INTERNAL_SUBNET:-}"
        if [ -z "$internal_subnet" ]; then
            internal_subnet="$(detect_internal_subnet || true)"
        fi
        if [ -n "$internal_subnet" ]; then
            export NGINX_DENY_INTERNAL_EXPORTS_DIRECTIVE="deny ${internal_subnet};
            allow all;"
        else
            echo "nginx-service: ROUTER_NGINX_DENY_INTERNAL_EXPORTS is on but couldn't determine code-docker-internal's subnet (ROUTER_INTERNAL_SUBNET unset and auto-detection failed) - skipping the deny" >&2
            export NGINX_DENY_INTERNAL_EXPORTS_DIRECTIVE=""
        fi
        ;;
esac

# ROUTER_MANAGER_HOSTS (router/example-env.router, comma-separated, default
# empty) - builds an entire extra server{} block (see
# router/config/nginx/nginx.default.conf's own comment on this placeholder)
# giving router-manager a dedicated origin, separate from the shared
# /router/ path. Empty means the placeholder substitutes to nothing and the
# main config is unchanged from before this feature existed - deliberately
# off by default, same as every other opt-in feature in this repo.
if [ -n "${ROUTER_MANAGER_HOSTS:-}" ]; then
    IFS=',' read -ra router_manager_hosts <<< "$ROUTER_MANAGER_HOSTS"
    server_names=""
    for host in "${router_manager_hosts[@]}"; do
        host="$(echo "$host" | xargs)"
        [ -n "$host" ] && server_names="$server_names $host"
    done
    if [ -n "$server_names" ]; then
        export NGINX_ROUTER_MANAGER_SERVER_BLOCK="server {
        listen 80;
        server_name$server_names;

        if (\$code_docker_loopback_blocked) {
            return 403 \"blocked: reached via tailscale's automatic loopback forwarding, not the published port - see NGINX_BLOCK_LOOPBACK in example-env\n\";
        }

        # Same as the shared hostname's own server block: the VNC tab's RFB
        # bridge is a long-lived WebSocket that can legitimately sit idle
        # (a desktop with nothing moving on it sends nothing), and nginx's
        # 60s default would cut it.
        proxy_read_timeout 3600s;

        # router/frontend's own api/client.ts hardcodes every API call under
        # /router/api/... regardless of which origin the SPA is actually
        # served from (see router/frontend/src/api/client.ts) - so this
        # dedicated domain needs the exact same /router/ prefix-strip
        # location as the shared hostname's own copy
        # (router/config/nginx/nginx.default.conf), even though the SPA
        # itself is served from this domain's root below, not /router/.
        location /router/ {
            proxy_pass http://unix:/run/router-manager.sock:/;
            proxy_set_header Host \$host;
            # Same WebSocket upgrade the shared hostname's own /router/
            # location needs - the VNC tab's RFB bridge lives under this
            # prefix. See nginx.default.conf's copy.
            proxy_http_version 1.1;
            proxy_set_header Upgrade \$http_upgrade;
            proxy_set_header Connection \$connection_upgrade;
        }

        # The SPA itself (and its relative-pathed JS/CSS - see
        # router/frontend/vite.config.ts's base setting) at this domain's
        # root, so router-manager is reachable as a real standalone site
        # without needing the /router/ segment at all.
        location / {
            proxy_pass http://unix:/run/router-manager.sock:/;
            proxy_http_version 1.1;
            proxy_set_header Upgrade \$http_upgrade;
            proxy_set_header Connection \"upgrade\";
            proxy_set_header Host \$host;
        }
    }"
    else
        export NGINX_ROUTER_MANAGER_SERVER_BLOCK=""
    fi
else
    export NGINX_ROUTER_MANAGER_SERVER_BLOCK=""
fi

# TINYAUTH_HOSTS (router/example-env.router, comma-separated, default
# empty) - dedicated hostname(s) serving tinyauth's own login UI. See
# router/config/nginx/nginx.default.conf's own comment on this placeholder
# for why tinyauth needs a whole hostname rather than a path, and
# router/docs/router.md#tinyauth for the setup.
#
# Warned about rather than silently ignored when TINYAUTH_APPURL is set
# without this: that combination is exactly the state router shipped in
# before this block existed, and its symptom (every "인증 요구" target
# answering 400/redirecting into code-server instead of a login page) gives
# no hint at all about what's missing.
if [ -n "${TINYAUTH_HOSTS:-}" ]; then
    IFS=',' read -ra tinyauth_hosts <<< "$TINYAUTH_HOSTS"
    tinyauth_server_names=""
    for host in "${tinyauth_hosts[@]}"; do
        host="$(echo "$host" | xargs)"
        [ -n "$host" ] && tinyauth_server_names="$tinyauth_server_names $host"
    done
else
    tinyauth_server_names=""
fi

if [ -n "$tinyauth_server_names" ]; then
    export NGINX_TINYAUTH_SERVER_BLOCK="server {
        listen 80;
        server_name$tinyauth_server_names;

        if (\$code_docker_loopback_blocked) {
            return 403 \"blocked: reached via tailscale's automatic loopback forwarding, not the published port - see NGINX_BLOCK_LOOPBACK in example-env\n\";
        }

        # tinyauth's own SPA + API at this domain's root. It has no base-path
        # setting, so root is the only place it can be served from.
        location / {
            proxy_pass http://127.0.0.1:3000;
            proxy_http_version 1.1;
            proxy_set_header Host \$host;
            proxy_set_header X-Forwarded-Proto \$router_forwarded_proto;
            proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        }
    }"
else
    export NGINX_TINYAUTH_SERVER_BLOCK=""
    if [ -n "${TINYAUTH_APPURL:-}" ]; then
        echo "nginx-service: TINYAUTH_APPURL is set but TINYAUTH_HOSTS is not - nothing serves tinyauth's login page, so every 'require auth' Dev Proxy route / App Route / VNC target will fail to log in. See router/docs/router.md#tinyauth" >&2
    fi
fi

# ROUTER_VHOST_<NAME> (one env var per entry, value "<host>[,<host>...]=<upstream>")
# - gives a container that isn't router's own SPA a whole hostname of its
# own, the same shape ROUTER_MANAGER_HOSTS and TINYAUTH_HOSTS already give
# router-manager and tinyauth: a plain server{} block selected by nginx's
# server_name matching, so your outer reverse proxy points that hostname at
# routerip:80 with no path rewrite, exactly like the other two.
#
# Why a scanned prefix rather than one comma-separated list: each entry is
# declared by whichever side project needs it, in that project's own compose
# overlay (`environment:` merged onto the code-docker-router service), so two
# projects attached at once must not have to merge into one shared value.
# One var each is additive by construction - the same reason netinit moved
# from NETFILTER_FIX_EXTRA_INTERNAL_NETWORKS to per-network Docker labels.
#
# This is deliberately NOT Dev Proxy: Dev Proxy's own Host matching lives
# inside caddy-adapter, which is only reachable through the /exports/ path
# prefix (or a published CADDY_ADAPTER_PORT), so serving a side project at
# the *root* of its own hostname through Dev Proxy needs a rewrite line in
# your outer proxy. It also means no per-route tinyauth here - a vhost is
# proxied straight through, and auth is your outer proxy's job (or move the
# target to Dev Proxy if you want tinyauth's per-route "인증 요구" instead).
# Docker's embedded DNS, read out of our own resolv.conf rather than
# hardcoded as 127.0.0.11, since router rewrites that file itself when the
# netgate is on. Needed because a vhost's upstream is another *container*:
# with a literal `proxy_pass http://trilium:8080`, nginx resolves the name
# once at startup and refuses to start at all when that container isn't up
# yet - which would take router (the whole front door) down because a side
# project was stopped. Passing the upstream through a variable defers
# resolution to request time, and that form needs an explicit resolver.
# IPv6 nameservers have to be bracketed in a `resolver` directive, or nginx
# rejects the config outright - and resolv.conf does carry them (tailscale's
# fd7a:115c:a1e0::53 sits next to 100.100.100.100 on a tailnet host).
vhost_resolver="$(awk '/^nameserver/ { print $2 }' /etc/resolv.conf 2>/dev/null \
    | awk '{ if (index($0, ":")) printf "[%s] ", $0; else printf "%s ", $0 }' | xargs || true)"

vhost_blocks=""
for vhost_var in $(compgen -v ROUTER_VHOST_ 2>/dev/null); do
    # ROUTER_VHOST_PWA_* / ROUTER_VHOST_PWA_ICON_* share this prefix but are
    # read below as part of whichever entry they belong to, not as entries of
    # their own - without this they'd each draw a "malformed entry" warning
    # that reads exactly like a real misconfiguration.
    case "$vhost_var" in
        ROUTER_VHOST_PWA_*) continue ;;
    esac

    vhost_value="${!vhost_var}"
    vhost_name="${vhost_var#ROUTER_VHOST_}"
    # An empty value is how a user turns one of these off without deleting
    # the line (same convention as OOTB_GENERATE_SECRETS keys), so it is not
    # an error - but say so, because a silent skip reads exactly like a
    # working route that just doesn't work.
    if [ -z "$vhost_value" ]; then
        echo "nginx-service: $vhost_var is empty - skipping (that hostname will fall through to DEFAULT_UPSTREAM)" >&2
        continue
    fi
    if [[ "$vhost_value" != *=* ]]; then
        echo "nginx-service: $vhost_var must look like '<host>[,<host>...]=<upstream>[:port]', got '$vhost_value' - skipping" >&2
        continue
    fi
    vhost_hosts="${vhost_value%%=*}"
    vhost_upstream="${vhost_value#*=}"

    # Both halves are pasted straight into an nginx config, so the charset
    # check is what keeps a stray `;` or newline from becoming a directive.
    # Same reasoning as backend/internal/targetguard's own charset check on
    # Dev Proxy / App Routes targets, just at the config-generation layer.
    if ! [[ "$vhost_upstream" =~ ^[A-Za-z0-9_.-]+(:[0-9]+)?$ ]]; then
        echo "nginx-service: $vhost_var upstream '$vhost_upstream' is not a bare host[:port] - skipping" >&2
        continue
    fi
    # Loopback is router's own inside: tinyauth (127.0.0.1:3000) and every
    # unix socket live there, and nothing a side project legitimately wants
    # to publish does. Refused for the same reason targetguard refuses it
    # for API-registered targets - a public hostname pointed at router's own
    # login service is a footgun, not a feature.
    case "${vhost_upstream%%:*}" in
        localhost|127.0.0.1|::1|router|forward)
            echo "nginx-service: $vhost_var upstream '$vhost_upstream' points at router itself - refusing" >&2
            continue
            ;;
    esac

    vhost_server_names=""
    vhost_bad=""
    IFS=',' read -ra vhost_host_list <<< "$vhost_hosts"
    for host in "${vhost_host_list[@]}"; do
        host="$(echo "$host" | xargs)"
        [ -n "$host" ] || continue
        if ! [[ "$host" =~ ^[A-Za-z0-9_.*-]+$ ]]; then
            echo "nginx-service: $vhost_var hostname '$host' has characters that can't appear in a server_name - skipping this entry" >&2
            vhost_bad=1
            break
        fi
        vhost_server_names="$vhost_server_names $host"
    done
    [ -n "$vhost_bad" ] && continue
    if [ -z "$vhost_server_names" ]; then
        echo "nginx-service: $vhost_var has no hostname before the '=' - skipping" >&2
        continue
    fi

    if [ -z "$vhost_resolver" ]; then
        echo "nginx-service: no nameserver in /etc/resolv.conf - $vhost_var will fail to resolve '$vhost_upstream' at request time" >&2
        vhost_resolver_directive=""
    else
        vhost_resolver_directive="resolver $vhost_resolver valid=10s ipv6=off;"
    fi

    # ROUTER_VHOST_PWA_<NAME>="<app name>|<short name>[|<manifest path>]" -
    # rewrites the app's own PWA manifest so a second instance of an app the
    # user already has installed is tellable apart on the home screen (the
    # concrete case: a personal Trilium and a project-scoped one both serving
    # "Trilium Notes" and the same icon). The merge itself is router-manager's
    # (backend/internal/vhostpwa); all this needs from the value is which path
    # to intercept, since only the app knows that (/manifest.webmanifest for
    # most, /manifest.json for code-server).
    vhost_pwa_var="ROUTER_VHOST_PWA_${vhost_name}"
    vhost_pwa_value="${!vhost_pwa_var:-}"
    vhost_pwa_block=""
    if [ -n "$vhost_pwa_value" ]; then
        vhost_pwa_key="$(printf '%s' "$vhost_name" | tr 'A-Z_' 'a-z-')"
        vhost_pwa_manifest="$(printf '%s' "$vhost_pwa_value" | awk -F'|' '{ print $3 }' | xargs)"
        [ -n "$vhost_pwa_manifest" ] || vhost_pwa_manifest="/manifest.webmanifest"
        vhost_pwa_icon_var="ROUTER_VHOST_PWA_ICON_${vhost_name}"
        vhost_pwa_icon="${!vhost_pwa_icon_var:-}"

        if ! [[ "$vhost_pwa_manifest" =~ ^/[A-Za-z0-9._/-]*$ ]]; then
            echo "nginx-service: ROUTER_VHOST_PWA_${vhost_name} manifest path '$vhost_pwa_manifest' is not a plain path - not rewriting the manifest" >&2
        else
            vhost_pwa_icon_block=""
            if [ -n "$vhost_pwa_icon" ]; then
                # Checked here rather than left to fail at request time: a
                # missing icon file otherwise shows up only as an app that
                # installs with the wrong picture, with nothing said anywhere.
                if ! [[ "$vhost_pwa_icon" =~ ^/[A-Za-z0-9._/-]+$ ]]; then
                    echo "nginx-service: ROUTER_VHOST_PWA_ICON_${vhost_name} '$vhost_pwa_icon' is not a plain absolute path - ignoring the icon" >&2
                elif [ ! -f "$vhost_pwa_icon" ]; then
                    echo "nginx-service: ROUTER_VHOST_PWA_ICON_${vhost_name} points at '$vhost_pwa_icon', which does not exist in this container - ignoring the icon (mount it under ROUTER_VOLUME)" >&2
                else
                    vhost_pwa_icon_block="
        # The replacement icon, at the fixed path the rewritten manifest
        # points at. Must be reachable with no credentials for an Android
        # install to work at all - the WebAPK is built by Google's servers,
        # not the phone - so leave this path out of your outer proxy's auth
        # the same way you already do for the manifest.
        location = /_pwa-icon.png {
            alias $vhost_pwa_icon;
            default_type image/png;
        }
"
                fi
            fi
            vhost_pwa_block="
        # PWA manifest rewrite (ROUTER_VHOST_PWA_${vhost_name}).
        location = $vhost_pwa_manifest {
            proxy_intercept_errors on;
            error_page 502 503 504 = @${vhost_pwa_key//-/_}_manifest_fallback;
            proxy_pass http://unix:/run/router-manager.sock:/api/vhost-pwa/$vhost_pwa_key/manifest;
            proxy_set_header Host \$host;
        }

        # Only reachable through the error_page above. If router-manager is
        # down or the merge fails, the app's own manifest is served unchanged
        # - the PWA still installs, just under the app's own name, instead of
        # not installing at all.
        location @${vhost_pwa_key//-/_}_manifest_fallback {
            internal;
            $vhost_resolver_directive
            set \$vhost_upstream $vhost_upstream;
            proxy_pass http://\$vhost_upstream;
            proxy_set_header Host \$host;
        }
$vhost_pwa_icon_block"
            echo "nginx-service: vhost $vhost_pwa_key rewrites its PWA manifest at $vhost_pwa_manifest" >&2
        fi
    fi

    echo "nginx-service: vhost${vhost_server_names} -> $vhost_upstream (from $vhost_var)" >&2
    vhost_blocks="$vhost_blocks
    server {
        listen 80;
        server_name$vhost_server_names;

        if (\$code_docker_loopback_blocked) {
            return 403 \"blocked: reached via tailscale's automatic loopback forwarding, not the published port - see NGINX_BLOCK_LOOPBACK in example-env\n\";
        }

        # Same two as the catch-all server block below: an upload cap of 0
        # (unlimited - the app decides), and a read timeout long enough for
        # an idle WebSocket. Both matter for a general-purpose app served
        # here; Trilium, the first user of this, holds a WebSocket open for
        # live updates and accepts large attachments.
        client_max_body_size 0;
        proxy_read_timeout 3600s;
$vhost_pwa_block
        location / {
            $vhost_resolver_directive
            # Through a variable (see vhost_resolver above), so a stopped
            # side project is a 502 on its own hostname instead of an nginx
            # that won't start. \$request_uri has to be spelled out because
            # nginx can't infer the passed URI once proxy_pass holds a
            # variable.
            set \$vhost_upstream $vhost_upstream;
            proxy_pass http://\$vhost_upstream\$request_uri;
            proxy_http_version 1.1;
            proxy_set_header Upgrade \$http_upgrade;
            proxy_set_header Connection \"upgrade\";
            proxy_set_header Host \$host;
            proxy_set_header X-Real-IP \$remote_addr;
            proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto \$router_forwarded_proto;
            # A vhost target is a whole separate origin, so it never has a
            # legitimate reason to see router-manager's admin unlock cookie
            # - stripped here for the same reason /exports/ and /app/ strip
            # it, even though a separate origin means the browser usually
            # would not attach it in the first place.
            proxy_set_header Cookie \$router_manager_cookie_stripped;
        }
    }"
done
export NGINX_VHOST_SERVER_BLOCKS="$vhost_blocks"

generated_config=/run/nginx.generated.conf
envsubst '${NGINX_ACCESS_LOG_IF} ${NGINX_ALLOWED_HOSTS_MAP} ${NGINX_ALLOWED_EXPORT_HOSTS_MAP} ${NGINX_LOOPBACK_BLOCK_MAP} ${NGINX_TRUSTED_PROXIES_DIRECTIVES} ${NGINX_DENY_INTERNAL_EXPORTS_DIRECTIVE} ${NGINX_ROUTER_MANAGER_SERVER_BLOCK} ${NGINX_TINYAUTH_SERVER_BLOCK} ${NGINX_VHOST_SERVER_BLOCKS}' < "$nginx_config" > "$generated_config"

exec nginx -g "daemon off;" -c "$generated_config"
