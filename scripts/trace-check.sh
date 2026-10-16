#!/usr/bin/env bash
# Usage: trace-check.sh REQUIREMENTS.md <test-dir>...
# Fails if a requirement has no "Verifies: <ID>" tag in tests, or a tag names an unknown ID.
set -euo pipefail
req="$1"; shift
[ "$#" -gt 0 ] || { echo "usage: $0 REQUIREMENTS.md <test-dir>..." >&2; exit 2; }

ids=$(perl -0pe 's/<!--.*?-->//gs' "$req" | grep --color=never -oE '^\| *[A-Z]+-[0-9]{3} *\|' | grep --color=never -oE '[A-Z]+-[0-9]{3}' | sort -u || true)
tags=$(grep --color=never -rhoE 'Verifies: [A-Z]+-[0-9]{3}' "$@" 2>/dev/null | awk '{print $2}' | sort -u || true)

untested=$(comm -23 <(echo "$ids") <(echo "$tags") | sed '/^$/d')
unknown=$(comm -13 <(echo "$ids") <(echo "$tags") | sed '/^$/d')

echo "requirements: $(echo "$ids" | sed '/^$/d' | wc -l | tr -d ' ')  tagged: $(echo "$tags" | sed '/^$/d' | wc -l | tr -d ' ')"
[ -z "$untested" ] || { echo "untested:"; echo "$untested" | sed 's/^/  /'; }
[ -z "$unknown" ] || { echo "unknown IDs in tests:"; echo "$unknown" | sed 's/^/  /'; }
[ -z "$untested$unknown" ]
