#!/bin/bash
# Checks nginx.default.conf's $router_prefix_escaped and
# $router_first_segment_changed maps against requests that must and must not be
# refused. Run by dev-check.sh (every tracked *_test.sh);
# the Dockerfile copies config/nginx file by file, so it stays out of the image.
#
# The patterns are read straight out of the config, so this tests what nginx
# actually gets. pcre2grep stands in for nginx's PCRE, and each sample is the
# map's key: the raw request URI, a space, then the $uri nginx normalizes it to.
# The second map's samples go in with -M and printf '%s', so the whole sample is
# one subject as in nginx, and a $uri ending in a decoded newline can be tested.
set -u
cd "$(dirname "$0")" || exit 1

mapfile -t patterns < <(sed -n '/\$router_prefix_escaped {$/,/^ *}$/s/^ *"~\(.*\)" 1;$/\1/p' nginx.default.conf)
if [ ${#patterns[@]} -lt 3 ]; then
    echo "found only ${#patterns[@]} patterns in \$router_prefix_escaped - did its layout change?" >&2
    exit 1
fi

escaped() {
    local p
    for p in "${patterns[@]}"; do
        printf '%s\n' "$1" | pcre2grep -q -- "$p" && return 0
    done
    return 1
}

failed=0
while IFS= read -r sample; do
    escaped "$sample" || { echo "should be refused but passes: $sample"; failed=1; }
done <<'EOF'
/exports/../router/ /router/
/exports/..%2frouter/ /router/
/exports/%2e%2e/router/api/auth/status /router/api/auth/status
/exports/foo/..%2f..%2frouter/ /router/
/exports/.. /
/exports/..%2fmanager/?a=/exports/ /manager/
/exports/../exports2/ /exports2/
/exports/../app/code/ /app/code/
/app/code/../../router/ /router/
/app/code/..%2f..%2fmanager/ /manager/
/app/code/../other/ /app/other/
/app/code/.. /app/
/app/code/../codex/ /app/codex/
/app/..%2f..%2fmanager /manager
/app/x%2f..%2f../router/ /router/
EOF

# Raw control characters nginx lets through in a URI, written with $'...' since
# they can't be typed into the heredoc above. Each is popped by the ../ after it.
for ctl in $'\t' $'\v' $'\f'; do
    for sample in "/exports/$ctl/..%2frouter/ /router/" "/app/code/$ctl/../../router/ /router/" \
        "/app/$ctl/../../router/ /router/"; do
        escaped "$sample" || { echo "should be refused but passes: $(printf '%q' "$sample")"; failed=1; }
    done
done

while IFS= read -r sample; do
    escaped "$sample" && { echo "should pass but is refused: $sample"; failed=1; }
done <<'EOF'
/ /
/router/ /router/
/manager/ /manager/
/../router/ /router/
/exports /exports
/exports/ /exports/
/exports/foo/bar?x=/../y /exports/foo/bar
/exports/foo/../bar /exports/bar
/exports/./foo /exports/foo
/exports//foo /exports/foo
/exports/foo/../../exports/bar /exports/bar
/exportsfoo/../router/ /router/
/app/code/ /app/code/
/app/code /app/code
/app/code?x=/y /app/code
/app/code/foo/../bar /app/code/bar
/app/code/. /app/code/
EOF

mapfile -t segment_patterns < <(sed -n '/\$router_first_segment_changed {$/,/^ *}$/s/^ *"~\(.*\)" 1;$/\1/p' nginx.default.conf)
if [ ${#segment_patterns[@]} -lt 2 ]; then
    echo "found only ${#segment_patterns[@]} patterns in \$router_first_segment_changed - did its layout change?" >&2
    exit 1
fi

changed() {
    local p
    for p in "${segment_patterns[@]}"; do
        printf '%s' "$1" | pcre2grep -M -q -- "$p" && return 0
    done
    return 1
}

# Grouped by class: dot-segments, encoded slashes, encoded dots, repeated
# slashes, dot-slash, a percent-encoded first segment, and a first segment that
# keeps its name but loses the slash after it (falls out of /router/ or
# /exports/ into the catch-all).
while IFS= read -r sample; do
    changed "$sample" || { echo "should be refused but passes: $sample"; failed=1; }
done <<'EOF'
/_static/../router/ /router/
/_static/x/../../router/api/auth/status /router/api/auth/status
/manifest.json/../exports/x /exports/x
/manifest.json/../app/code/ /app/code/
/_static/.. /
/router/.. /
/../router/ /router/
/_static/..%2frouter/ /router/
/_static%2f..%2frouter/ /router/
/_static/..%2F..%2Fapp/code/ /app/code/
/manifest.json%2f..%2fexports/ /exports/
/_static/%2e%2e/router/ /router/
/_static/%2E%2E/exports/x /exports/x
/_static/.%2e/router/ /router/
//router/ /router/
// /
///exports/x /exports/x
//_static/x /_static/x
/./router/ /router/
/./ /
/_static/./../router/ /router/
/%72outer/ /router/
/%5fstatic/x /_static/x
/%5Fstatic/x /_static/x
/manifest%2ejson /manifest.json
/%2e%2e/router/ /router/
/%2e/router/ /router/
/%65xports/x /exports/x
/router/..%2frouter /router
/exports/..%2fexports /exports
/_static/..%2f_static /_static
/router/../router?x=/ /router
/_static/..%2frouter/?a=/_static/ /router/
EOF

# Raw control characters nginx lets through in a URI, each popped by the ../
# after it, and a $uri ending in a decoded newline.
for ctl in $'\t' $'\v' $'\f'; do
    for sample in "/_static/$ctl/..%2frouter/ /router/" "/_static/$ctl/../../router/ /router/" \
        "/$ctl/../router/ /router/"; do
        changed "$sample" || { echo "should be refused but passes: $(printf '%q' "$sample")"; failed=1; }
    done
done
for sample in $'/router/../router%0a /router\n' $'/router%0a /router\n'; do
    changed "$sample" || { echo "should be refused but passes: $(printf '%q' "$sample")"; failed=1; }
done

while IFS= read -r sample; do
    changed "$sample" && { echo "should pass but is refused: $sample"; failed=1; }
done <<'EOF'
/ /
/?folder=/code/Projects /
/?a=/../router/ /
/router/ /router/
/router /router
/router/api/auth/status /router/api/auth/status
/router/api/vnc/targets/studio/ws /router/api/vnc/targets/studio/ws
/router/novnc/vnc.html?path=router/api/vnc/targets/x/ws /router/novnc/vnc.html
/manager/ /manager/
/manager /manager
/manager/api/files?path=/code/../etc /manager/api/files
/stable-0123abcd/static/out/vs/code/browser/workbench/workbench.js /stable-0123abcd/static/out/vs/code/browser/workbench/workbench.js
/stable-0123abcd?reconnectionToken=x&skipWebSocketFrames=false /stable-0123abcd
/_static/out/browser/serviceWorker.js /_static/out/browser/serviceWorker.js
/_static/out/./browser/x.js /_static/out/browser/x.js
/_static//out/x.js /_static/out/x.js
/_static/out/../src/x.png /_static/src/x.png
/_static/out/%2e%2e/src/x.png /_static/src/x.png
/_static/x.js?y=/../../router/ /_static/x.js
/manifest.json /manifest.json
/manifest.json?v=1 /manifest.json
/exports/ /exports/
/exports/foo/bar?x=/../y /exports/foo/bar
/exports/foo/../bar /exports/bar
/exports//foo /exports/foo
/exports/a%20b/%2e/c /exports/a b/c
/app/code/ /app/code/
/app/code /app/code
/app/code/foo/../bar /app/code/bar
/healthz /healthz
EOF
exit $failed
