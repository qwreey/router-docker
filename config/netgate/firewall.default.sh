#!/bin/sh
set -u

# supervisord program (see config/netgate/supervisord.default.conf) - loops
# forever re-applying netgate's iptables rules from config.yaml, rather than
# a one-shot init step, for two reasons: (1) a forward's target_host can
# resolve to a new IP after the target container restarts, so the DNAT rule
# needs periodic re-resolution, same self-healing idiom as
# netinit/script/netinit-entrypoint.sh; (2) code-docker-external's interface may not
# be up yet on the very first iteration, so retrying is simpler than a
# one-shot script with its own polling/timeout logic. See
# .claude/backlog/egress-netgate-plan.md for the overall design.

# resolve_config_path is called fresh every loop iteration (not once at
# script start) so this picks up the live, web-editable copy
# router-manager's Net 관리 탭 reads/writes (see
# router/backend/internal/netgate) as soon as router-manager has seeded it
# - netgate-firewall and router-manager are separate supervisord programs
# with no ordering guarantee between them, so this script can easily start
# first and would otherwise be stuck on the fallback path forever. Falls
# back to the old file-only selection only for the brief window before
# router-manager has run for the first time (or if it's ever disabled) -
# this keeps existing config.override.yaml-only deployments working
# unmodified until a live copy exists.
resolve_config_path() {
	live=/var/lib/code-docker-router/netgate/config.yaml
	if [ -e "$live" ]; then
		echo "$live"
	elif [ -e /etc/router/netgate/config.override.yaml ]; then
		echo /etc/router/netgate/config.override.yaml
	else
		echo /etc/router/netgate/config.default.yaml
	fi
}

# Each cycle flushes and rebuilds netgate's own chains (see apply_rules
# below) - this is idempotent but not atomic, so there's a brief window
# every cycle where the rebuilt chain is empty and FORWARD's default ACCEPT
# policy applies unfiltered. Same accepted trade-off qdm12/gluetun's
# firewall documents for the same pattern (their iptables rules apply after
# Docker's own network init, leaving a similar small window) - see the plan
# doc's research notes. Not attempting an atomic chain swap here to keep
# this script simple; revisit if the window ever proves large enough to
# matter in practice.
ensure_chain() {
	iptables -t "$1" -N "$2" 2>/dev/null || iptables -t "$1" -F "$2"
}

ensure_jump() {
	iptables -t "$1" -C "$2" -j "$3" 2>/dev/null || iptables -t "$1" -I "$2" 1 -j "$3"
}

# --- IPv6 (security-review H3 fix, 2026-09-14) --------------------------
#
# Every function/rule above and below this block is IPv4-only and
# deliberately untouched. IPv6 is off by default (ENABLE_IPV6 unset/false
# in example-env only configures docker-compose's own network defs, never
# passed into this container as an env var) - so detection here is dynamic
# (a live global-scope IPv6 address actually assigned to this container),
# not a re-read of that var, and in the common case none of this runs at
# all. This mirrors only the fixed always-block set (ULA/link-local/
# loopback, the v6 analogues of the v4 RFC1918/link-local/loopback set
# above) plus whatever outbound: entries in config.yaml happen to be v6
# CIDRs - config.default.yaml ships v4-only today, so that part is a no-op
# until config.yaml actually grows a v6 entry (config-driven v6 rules
# aren't a supported feature yet, this just means one won't be silently
# dropped later). forwards: (DNAT) and bandwidth: are NOT mirrored to v6 -
# out of scope for this fix, see docs/egress-netgate.md's IPv6 section.
ensure_chain6() {
	ip6tables -t "$1" -N "$2" 2>/dev/null || ip6tables -t "$1" -F "$2"
}

ensure_jump6() {
	ip6tables -t "$1" -C "$2" -j "$3" 2>/dev/null || ip6tables -t "$1" -I "$2" 1 -j "$3"
}

# A global-scope IPv6 address only shows up once Docker's own IPv6 network
# support has actually assigned one to this container - the real signal
# that IPv6 traffic can flow on this path at all, independent of whether
# ENABLE_IPV6 was ever set (which this script never sees directly). Every
# interface gets a link-local (fe80::/10, scope "link") address regardless,
# so filtering on scope global specifically is what keeps this from
# false-triggering on a stock IPv6-disabled deployment.
ipv6_present() {
	ip -6 addr show scope global 2>/dev/null | grep -q 'inet6'
}

# Loud AND persistent, per this repo's "no silent skips in setup scripts"
# rule - a single log line scrolls off and gets missed, so this prints a
# multi-line block, and apply_rules_v6 (called from the same 30s loop
# apply_rules already runs in) re-triggers it every cycle for as long as
# the condition holds, instead of warning once and going quiet.
warn_ipv6_unfiltered() {
	echo >&2 "=================================================================="
	echo >&2 "netgate-firewall: WARNING - IPv6 is active on this container but its"
	echo >&2 "netgate-firewall: WARNING - FORWARD traffic is NOT being filtered: $1"
	echo >&2 "netgate-firewall: WARNING - only IPv4 egress is protected right now."
	echo >&2 "netgate-firewall: WARNING - see router/docs/egress-netgate.md (IPv6 section)."
	echo >&2 "=================================================================="
}

# Called from apply_rules below, in the same process (not a subshell), so
# it shares apply_rules' own $config_snapshot/$out_count instead of
# re-parsing config.yaml a second time.
apply_rules_v6() {
	if ! ipv6_present; then
		# Nothing to filter and nothing to warn about: in this state no
		# traffic crosses the FORWARD chain over v6 at all.
		return 0
	fi

	if ! command -v ip6tables >/dev/null 2>&1; then
		warn_ipv6_unfiltered "ip6tables binary not found in this image"
		return 0
	fi

	ensure_chain6 filter NETGATE-FORWARD6
	ensure_jump6 filter FORWARD NETGATE-FORWARD6

	# Same reasoning as the v4 stateful-accept rule below: without this,
	# return traffic for an already-permitted v6 connection gets
	# re-evaluated by the block rules on its way back.
	if ! ip6tables -A NETGATE-FORWARD6 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT; then
		warn_ipv6_unfiltered "failed to insert the ip6tables stateful-accept rule"
		return 1
	fi

	# Fixed block set: ULA (fc00::/7, the v6 analogue of RFC1918),
	# link-local (fe80::/10) and loopback (::1/128) - applied
	# unconditionally, the same fixed set the v4 side hardcodes for
	# link-local/loopback rather than leaving it to config.yaml.
	ip6tables -A NETGATE-FORWARD6 -d fc00::/7 -j DROP || warn_ipv6_unfiltered "failed to apply the fc00::/7 (ULA) block rule"
	ip6tables -A NETGATE-FORWARD6 -d fe80::/10 -j DROP || warn_ipv6_unfiltered "failed to apply the fe80::/10 (link-local) block rule"
	ip6tables -A NETGATE-FORWARD6 -d ::1/128 -j DROP || warn_ipv6_unfiltered "failed to apply the ::1/128 (loopback) block rule"

	# Mirror outbound: only for entries that are themselves v6 CIDRs - a
	# bare ':' reliably tells a v6 CIDR from a v4 one (a dotted-quad CIDR
	# never contains one).
	v6_out_count=0
	i=0
	while [ "$i" -lt "$out_count" ]; do
		cidr=$(printf '%s' "$config_snapshot" | yq -r ".outbound[$i].cidr")
		case "$cidr" in
		*:*)
			action=$(printf '%s' "$config_snapshot" | yq -r ".outbound[$i].action")
			case "$action" in
			allow) ip6tables -A NETGATE-FORWARD6 -d "$cidr" -j ACCEPT ;;
			block) ip6tables -A NETGATE-FORWARD6 -d "$cidr" -j DROP ;;
			*) echo >&2 "netgate-firewall: unknown action '$action' for v6 cidr $cidr, skipping" ;;
			esac
			v6_out_count=$((v6_out_count + 1))
			;;
		esac
		i=$((i + 1))
	done

	echo "netgate-firewall: applied ip6tables fixed block set + $v6_out_count config-driven v6 outbound rule(s)"
}

apply_rules() {
	NETGATE_CONFIG="$(resolve_config_path)"
	default_iface="$(ip -4 route show default 2>/dev/null | awk '{ print $5; exit }')"
	if [ -z "$default_iface" ]; then
		echo >&2 "netgate-firewall: no default route yet (code-docker-external not up?), skipping this cycle"
		return 0
	fi

	# Snapshot the config once per cycle instead of letting every yq call
	# below re-open $NETGATE_CONFIG from disk independently: router-manager's
	# own config writes are now atomic (temp file + rename, see
	# internal/netgate's save()), so a single read here always sees either
	# the old or the new file in full - never a torn/partial one - and every
	# field extracted below is consistent with every other field from the
	# same cycle, which N independent re-reads couldn't guarantee.
	config_snapshot="$(cat "$NETGATE_CONFIG" 2>/dev/null)"

	ensure_chain filter NETGATE-FORWARD
	ensure_jump filter FORWARD NETGATE-FORWARD
	ensure_chain nat NETGATE-PREROUTING
	ensure_jump nat PREROUTING NETGATE-PREROUTING
	ensure_chain nat NETGATE-POSTROUTING
	ensure_jump nat POSTROUTING NETGATE-POSTROUTING

	iptables -t nat -A NETGATE-POSTROUTING -o "$default_iface" -j MASQUERADE

	# Stateful accept, always first: without this, RETURN traffic for any
	# already-permitted connection (e.g. the forwards: DNAT reply below, or
	# a code-docker outbound connection that was allowed on its way out)
	# gets re-evaluated by the ordered outbound: rules below on its way
	# back - and since Docker's own bridge subnets (code-docker-internal,
	# code-docker-external) are themselves RFC1918 addresses, that reply
	# traffic would get dropped by our own block rules. Confirmed by
	# testing: the forwards: DNAT to code-docker:80 matched correctly on
	# the way in, but the connection hung until this rule was added -
	# established/related bypassing re-evaluation is standard stateful
	# firewall practice and does not weaken NEW-connection filtering below
	# (a fresh SYN from code-docker to a private IP still hits NEW state
	# and gets evaluated normally).
	iptables -A NETGATE-FORWARD -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

	# Port forwards first - each one's FORWARD ACCEPT must land before the
	# outbound: block rules below, since target_host's own address is
	# typically itself in RFC1918 range (see config.default.yaml's
	# comment and the plan doc's "인바운드" section).
	fwd_count=$(printf '%s' "$config_snapshot" | yq -r '.forwards | length' 2>/dev/null)
	# Both a yq failure (nonzero exit, e.g. malformed YAML) and yq succeeding
	# with empty output (e.g. $config_snapshot itself was empty - the config
	# file doesn't exist yet) must fall back to 0 - an empty $fwd_count would
	# otherwise make the `[ "$i" -lt "$fwd_count" ]` test below error out.
	fwd_count="${fwd_count:-0}"
	i=0
	while [ "$i" -lt "$fwd_count" ]; do
		host_port=$(printf '%s' "$config_snapshot" | yq -r ".forwards[$i].host_port")
		target_host=$(printf '%s' "$config_snapshot" | yq -r ".forwards[$i].target_host")
		target_port=$(printf '%s' "$config_snapshot" | yq -r ".forwards[$i].target_port")
		target_ip="$(getent hosts "$target_host" 2>/dev/null | awk '{ print $1; exit }')"
		if [ -n "$target_ip" ]; then
			# -i "$default_iface" restricts this to traffic actually
			# arriving from the host/internet side - without it, an
			# outbound connection FROM code-docker-internal to some
			# unrelated host:80 on the internet would also match
			# --dport 80 and get DNATed back to target_host by mistake.
			iptables -t nat -A NETGATE-PREROUTING -i "$default_iface" -p tcp --dport "$host_port" -j DNAT --to-destination "$target_ip:$target_port"
			iptables -A NETGATE-FORWARD -d "$target_ip" -p tcp --dport "$target_port" -j ACCEPT
		else
			echo >&2 "netgate-firewall: forward #$i target '$target_host' does not resolve yet, skipping this cycle"
		fi
		i=$((i + 1))
	done

	out_count=$(printf '%s' "$config_snapshot" | yq -r '.outbound | length' 2>/dev/null)
	out_count="${out_count:-0}"
	i=0
	while [ "$i" -lt "$out_count" ]; do
		action=$(printf '%s' "$config_snapshot" | yq -r ".outbound[$i].action")
		cidr=$(printf '%s' "$config_snapshot" | yq -r ".outbound[$i].cidr")
		case "$action" in
		allow) iptables -A NETGATE-FORWARD -d "$cidr" -j ACCEPT ;;
		block) iptables -A NETGATE-FORWARD -d "$cidr" -j DROP ;;
		*) echo >&2 "netgate-firewall: unknown action '$action' for $cidr, skipping" ;;
		esac
		i=$((i + 1))
	done

	echo "netgate-firewall: applied $fwd_count forward(s), $out_count outbound rule(s) (external=$default_iface)"

	# v6 is applied last and never affects the return status of the v4
	# path above - a v6 warning/failure must not make apply_rules itself
	# look like it failed and get retried (the v4 rules it would be
	# retrying are already applied and fine).
	apply_rules_v6
	return 0
}

# net.ipv4.ip_forward=1 is set declaratively via docker-compose.yml's
# sysctls: on this service, not here - `sysctl -w` at runtime fails with
# permission denied even with NET_ADMIN, since Docker keeps /proc/sys
# read-only for non-privileged containers regardless of capabilities. See
# the compose file's own comment on this.

# Scoped here (not in netgate-entrypoint.sh, which used to `exec sleep
# infinity` for the whole router container the moment this was false) -
# NETGATE_ENABLED is meant to be a behavioral opt-out for egress filtering
# specifically, same tier as TAILSCALE_ENABLED/CADDY_ADAPTER_ENABLED, each
# of which only idles its own program. The old container-wide placement
# meant flipping this off also silently killed DNS/tailscale/Dev Proxy/
# tinyauth/router-manager itself, none of which have any relation to egress
# filtering. See root CLAUDE.md's code-quality audit.
if [ "${NETGATE_ENABLED:-true}" = "false" ]; then
	echo "netgate-firewall: NETGATE_ENABLED=false, idling without applying any firewall rules"
	exec sleep infinity
fi

while true; do
	apply_rules || echo >&2 "netgate-firewall: apply_rules failed this cycle, will retry"
	sleep 30
done
