#!/bin/bash
# Checks nginx.default.conf's $router_prefix_escaped map against requests that
# must and must not be refused. Run by dev-check.sh (every tracked *_test.sh);
# the Dockerfile copies config/nginx file by file, so it stays out of the image.
#
# The patterns are read straight out of the config, so this tests what nginx
# actually gets. pcre2grep stands in for nginx's PCRE, and each sample is the
# map's key: the raw request URI, a space, then the $uri nginx normalizes it to.
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
exit $failed
