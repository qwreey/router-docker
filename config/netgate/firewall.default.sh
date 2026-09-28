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

# Each cycle rebuilds netgate's own chains from scratch and commits them in one
# `iptables-restore --noflush` transaction per table: a `:CHAIN - [0:0]` line
# replaces that chain's rules atomically, other chains are left alone, and a
# transaction with any bad line is rejected whole, leaving the previous cycle's
# rules in place. Flushing and re-adding rule by rule instead left the chain
# empty for a moment every 30s, and FORWARD's default ACCEPT then let traffic
# to RFC1918/metadata addresses through.
ensure_jump() {
	iptables -t "$1" -C "$2" -j "$3" 2>/dev/null || iptables -t "$1" -I "$2" 1 -j "$3"
}

ensure_jump6() {
	ip6tables -t "$1" -C "$2" -j "$3" 2>/dev/null || ip6tables -t "$1" -I "$2" 1 -j "$3"
}

# Always blocked, ahead of every config.yaml outbound: entry, so no allow rule
# (or an outbound list emptied through the API) can reopen them: loopback,
# link-local (cloud metadata endpoints live at 169.254.169.254) and "this
# network". RFC1918 is deliberately NOT here - config.yaml blocks it, and a
# narrow allow ahead of that block is how a workload is given one LAN host;
# router-manager refuses an outbound list that drops those blocks entirely.
FIXED_BLOCK_V4="127.0.0.0/8 169.254.0.0/16 0.0.0.0/8"
FIXED_BLOCK_V6="fc00::/7 fe80::/10 ::1/128"

# Values from config.yaml are pasted into iptables-restore input, not passed as
# argv, so anything that could carry a newline or a second option is rejected
# here rather than trusted to router-manager's own validation (config.override.yaml
# is edited by hand).
# A bare address is valid too (router-manager accepts one; `-d 8.8.8.8` is a
# single-host rule). The character set alone is what keeps a value from
# carrying a newline or another option; a malformed-but-harmless value makes
# iptables-restore reject the cycle, loudly, leaving the previous rules.
valid_cidr() {
	case "$1" in
	'' | *[!0-9A-Fa-f:./]*) return 1 ;;
	*) return 0 ;;
	esac
}

valid_port() {
	case "$1" in
	'' | *[!0-9]*) return 1 ;;
	*) [ "$1" -ge 1 ] && [ "$1" -le 65535 ] ;;
	esac
}

# Rule text accumulated for this cycle's transactions.
add_filter() { filter_rules="$filter_rules$*
"; }
add_nat() { nat_rules="$nat_rules$*
"; }
add_filter6() { filter6_rules="$filter6_rules$*
"; }

# --- IPv6 (security-review H3 fix, 2026-09-14) --------------------------
#
# IPv6 is off by default (ENABLE_IPV6 unset/false in example-env only
# configures docker-compose's own network defs, never passed into this
# container as an env var) - so detection here is dynamic (a live
# global-scope IPv6 address actually assigned to this container), not a
# re-read of that var, and in the common case none of this runs at all. This
# mirrors only the fixed always-block set (FIXED_BLOCK_V6, the v6 analogues of
# the v4 RFC1918/link-local/loopback set) plus whatever outbound: entries in
# config.yaml happen to be v6 CIDRs. forwards: (DNAT) and bandwidth: are NOT
# mirrored to v6 - see docs/egress-netgate.md's IPv6 section.

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

	if ! command -v ip6tables-restore >/dev/null 2>&1; then
		warn_ipv6_unfiltered "ip6tables-restore binary not found in this image"
		return 0
	fi

	filter6_rules=""
	# Same reasoning as the v4 stateful-accept rule below.
	add_filter6 -A NETGATE-FORWARD6 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
	for cidr in $FIXED_BLOCK_V6; do
		add_filter6 -A NETGATE-FORWARD6 -d "$cidr" -j DROP
	done

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
			if ! valid_cidr "$cidr"; then
				echo >&2 "netgate-firewall: invalid v6 cidr '$cidr', skipping"
			else
				case "$action" in
				allow) add_filter6 -A NETGATE-FORWARD6 -d "$cidr" -j ACCEPT ;;
				block) add_filter6 -A NETGATE-FORWARD6 -d "$cidr" -j DROP ;;
				*) echo >&2 "netgate-firewall: unknown action '$action' for v6 cidr $cidr, skipping" ;;
				esac
				v6_out_count=$((v6_out_count + 1))
			fi
			;;
		esac
		i=$((i + 1))
	done

	if ! printf '*filter\n:NETGATE-FORWARD6 - [0:0]\n%sCOMMIT\n' "$filter6_rules" | ip6tables-restore --noflush; then
		warn_ipv6_unfiltered "ip6tables-restore rejected this cycle's rules (previous rules, if any, stay in place)"
		return 1
	fi
	ensure_jump6 filter FORWARD NETGATE-FORWARD6

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
	# own config writes are atomic (temp file + rename, see internal/netgate's
	# save()), so a single read here always sees either the old or the new
	# file in full, and every field below comes from the same version.
	config_snapshot="$(cat "$NETGATE_CONFIG" 2>/dev/null)"

	filter_rules=""
	nat_rules=""

	add_nat -A NETGATE-POSTROUTING -o "$default_iface" -j MASQUERADE

	# Stateful accept, always first: without this, RETURN traffic for any
	# already-permitted connection (e.g. the forwards: DNAT reply below, or
	# a code-docker outbound connection that was allowed on its way out)
	# gets re-evaluated by the ordered outbound: rules below on its way
	# back - and since Docker's own bridge subnets are themselves RFC1918
	# addresses, that reply traffic would get dropped by our own block
	# rules. A fresh SYN from code-docker to a private IP is still NEW and
	# gets evaluated normally.
	add_filter -A NETGATE-FORWARD -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

	# The fixed set next, so neither a forward's ACCEPT nor an outbound allow
	# can reopen it.
	for cidr in $FIXED_BLOCK_V4; do
		add_filter -A NETGATE-FORWARD -d "$cidr" -j DROP
	done

	# Port forwards next - each one's FORWARD ACCEPT must land before the
	# outbound: block rules below, since target_host's own address is
	# typically itself in RFC1918 range (see config.default.yaml's comment).
	fwd_count=$(printf '%s' "$config_snapshot" | yq -r '.forwards | length' 2>/dev/null)
	# Both a yq failure (nonzero exit, e.g. malformed YAML) and yq succeeding
	# with empty output (e.g. the config file doesn't exist yet) must fall
	# back to 0 - an empty $fwd_count would make the loop test error out.
	fwd_count="${fwd_count:-0}"
	i=0
	while [ "$i" -lt "$fwd_count" ]; do
		host_port=$(printf '%s' "$config_snapshot" | yq -r ".forwards[$i].host_port")
		target_host=$(printf '%s' "$config_snapshot" | yq -r ".forwards[$i].target_host")
		target_port=$(printf '%s' "$config_snapshot" | yq -r ".forwards[$i].target_port")
		target_ip="$(getent hosts "$target_host" 2>/dev/null | awk '{ print $1; exit }')"
		if ! valid_port "$host_port" || ! valid_port "$target_port"; then
			echo >&2 "netgate-firewall: forward #$i has an invalid port ($host_port -> $target_port), skipping"
		elif [ -z "$target_ip" ] || ! valid_cidr "$target_ip"; then
			echo >&2 "netgate-firewall: forward #$i target '$target_host' does not resolve yet, skipping this cycle"
		else
			# -i "$default_iface" restricts this to traffic actually
			# arriving from the host/internet side - without it, an
			# outbound connection FROM code-docker-internal to some
			# unrelated host:80 on the internet would also match
			# --dport 80 and get DNATed back to target_host by mistake.
			add_nat -A NETGATE-PREROUTING -i "$default_iface" -p tcp --dport "$host_port" -j DNAT --to-destination "$target_ip:$target_port"
			add_filter -A NETGATE-FORWARD -d "$target_ip" -p tcp --dport "$target_port" -j ACCEPT
		fi
		i=$((i + 1))
	done

	out_count=$(printf '%s' "$config_snapshot" | yq -r '.outbound | length' 2>/dev/null)
	out_count="${out_count:-0}"
	i=0
	while [ "$i" -lt "$out_count" ]; do
		action=$(printf '%s' "$config_snapshot" | yq -r ".outbound[$i].action")
		cidr=$(printf '%s' "$config_snapshot" | yq -r ".outbound[$i].cidr")
		case "$cidr" in
		*:*) ;; # v6 - apply_rules_v6's job
		*)
			if ! valid_cidr "$cidr"; then
				echo >&2 "netgate-firewall: invalid cidr '$cidr', skipping"
			else
				case "$action" in
				allow) add_filter -A NETGATE-FORWARD -d "$cidr" -j ACCEPT ;;
				block) add_filter -A NETGATE-FORWARD -d "$cidr" -j DROP ;;
				*) echo >&2 "netgate-firewall: unknown action '$action' for $cidr, skipping" ;;
				esac
			fi
			;;
		esac
		i=$((i + 1))
	done

	# Filter before nat: for the moment between the two commits, the new
	# DNAT target can only be dropped by the old filter (fail closed), never
	# let through by something the new filter would refuse.
	if ! printf '*filter\n:NETGATE-FORWARD - [0:0]\n%sCOMMIT\n*nat\n:NETGATE-PREROUTING - [0:0]\n:NETGATE-POSTROUTING - [0:0]\n%sCOMMIT\n' "$filter_rules" "$nat_rules" | iptables-restore --noflush; then
		echo >&2 "netgate-firewall: iptables-restore rejected this cycle's rules - the previous cycle's rules stay in place"
		return 1
	fi
	ensure_jump filter FORWARD NETGATE-FORWARD
	ensure_jump nat PREROUTING NETGATE-PREROUTING
	ensure_jump nat POSTROUTING NETGATE-POSTROUTING

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
