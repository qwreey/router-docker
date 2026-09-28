#!/bin/sh
set -eu

# NETGATE_ENABLED's own opt-out check moved to firewall.default.sh - this
# script used to idle the ENTIRE router container (never starting
# supervisord at all) when NETGATE_ENABLED=false, which also silently took
# down DNS/tailscale/Dev Proxy/tinyauth/router-manager along with egress
# filtering, unlike TAILSCALE_ENABLED/CADDY_ADAPTER_ENABLED which only ever
# idle their own program. See root CLAUDE.md's code-quality audit and
# firewall.default.sh's own comment on its new check.

# Front door: router's nginx (the only thing behind host:80 - code-server,
# webmanager, App Routes, Dev Proxy, vhosts) must only be reachable from the
# network that faces outward, i.e. the one carrying the default route
# (code-docker-external, where a published `ports:` entry also lands - measured,
# since internal: true networks are never port-publish targets) plus loopback
# (tailscaled's netstack forwards tailnet connections to 127.0.0.1). Every other
# interface is a network router joined to BE a gateway or a VNC relay for
# something behind the fence - code-docker-internal, and every sibling's own
# isolated network - and nginx listening on 0.0.0.0 used to turn router into a
# way back in from all of them: measured 2026-09-28, a fetcher on
# code-docker-firecrawl's isolated network got code-server's 302 from
# http://router/ and a /code file listing from /manager/api/files/list. The
# older per-location "deny internal subnets" (ROUTER_NGINX_DENY_INTERNAL_*) only
# ever covered /exports/ and /router/.
#
# An iptables INPUT rule keyed on the interface rather than nginx `listen <ip>`:
# one wildcard `listen 80` added to any server block later would silently
# reopen the IP-based version, the interface rule doesn't care what nginx binds.
# Applied here, synchronously, before supervisord starts nginx, so there is no
# window where the front door is open - and not in netgate-firewall, which
# NETGATE_ENABLED=false idles. Failing to apply it exits the container rather
# than serving an open front door quietly; ROUTER_FRONTDOOR_EXTERNAL_ONLY=false
# is the deliberate opt-out. DNS (53) and tailscale forwards (socat on the
# `forward` alias) are internal-facing on purpose and are not in the port list.
guard_front_door() {
	ports="${ROUTER_FRONTDOOR_PORTS:-80}"
	ext_if="$(ip -4 route show default 2>/dev/null \
		| awk '{for (i=1;i<=NF;i++) if ($i=="dev") { print $(i+1); exit }}')"
	if [ -z "$ext_if" ]; then
		echo "netgate-entrypoint: no IPv4 default route, so no outward-facing interface to restrict the front door (ports $ports) to - refusing to start with it open. Set ROUTER_FRONTDOOR_EXTERNAL_ONLY=false to run anyway." >&2
		return 1
	fi
	for ipt in iptables ip6tables; do
		if ! command -v "$ipt" >/dev/null 2>&1; then
			[ "$ipt" = ip6tables ] && { echo "netgate-entrypoint: ip6tables not found - front door guarded on IPv4 only (nginx has no IPv6 listener by default)" >&2; continue; }
			echo "netgate-entrypoint: $ipt not found" >&2
			return 1
		fi
		"$ipt" -N ROUTER-FRONTDOOR 2>/dev/null || "$ipt" -F ROUTER-FRONTDOOR || return 1
		"$ipt" -C INPUT -j ROUTER-FRONTDOOR 2>/dev/null || "$ipt" -I INPUT 1 -j ROUTER-FRONTDOOR || return 1
		for port in $(printf '%s' "$ports" | tr ',' ' '); do
			"$ipt" -A ROUTER-FRONTDOOR -p tcp --dport "$port" -i lo -j RETURN || return 1
			"$ipt" -A ROUTER-FRONTDOOR -p tcp --dport "$port" -i "$ext_if" -j RETURN || return 1
			"$ipt" -A ROUTER-FRONTDOOR -p tcp --dport "$port" -j REJECT --reject-with tcp-reset || return 1
		done
	done
	echo "netgate-entrypoint: front door (tcp $ports) accepts only $ext_if (outward-facing) and lo"
}

if [ "${ROUTER_FRONTDOOR_EXTERNAL_ONLY:-true}" = "false" ]; then
	echo "netgate-entrypoint: ROUTER_FRONTDOOR_EXTERNAL_ONLY=false - front door reachable from every attached network, including code-docker-internal and every sibling's isolated network" >&2
elif ! guard_front_door; then
	echo "netgate-entrypoint: could not guard the front door - exiting (see above)" >&2
	exit 1
fi

if [ -e /etc/router/netgate/supervisord.override.conf ]; then
	exec /sbin/supervisord -n -c /etc/router/netgate/supervisord.override.conf --user root
else
	exec /sbin/supervisord -n -c /etc/router/netgate/supervisord.default.conf --user root
fi
