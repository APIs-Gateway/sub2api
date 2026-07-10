#!/usr/bin/env bash
# Collapse duplicate blocks emitted by `go test -coverpkg=./... ./...`.
#
# Each package test process writes an entry for every instrumented package. A
# later process that did not exercise a block reports zero hits, so consumers
# that keep the first/last duplicate can under-report real coverage. Retain one
# entry per block with the highest hit count: the block is covered if any test
# process exercised it.
set -euo pipefail

if [ "$#" -ne 1 ]; then
	printf 'usage: %s <coverprofile>\n' "$0" >&2
	exit 2
fi

profile=$1
tmp_profile="${profile}.normalized.$$"
trap 'rm -f "$tmp_profile"' EXIT

awk '
NR == 1 {
	if ($1 != "mode:") {
		printf "invalid Go coverprofile header: %s\\n", $0 > "/dev/stderr"
		exit 1
	}
	mode = $2
	next
}
{
	key = $1 SUBSEP $2
	if (!(key in seen)) {
		seen[key] = ++count
		order[count] = key
		location[key] = $1
		statements[key] = $2
	}
	if ($3 > hits[key]) {
		hits[key] = $3
	}
}
END {
	if (mode == "") {
		exit 1
	}
	print "mode:", mode
	for (i = 1; i <= count; i++) {
		key = order[i]
		print location[key], statements[key], hits[key]
	}
}
' "$profile" > "$tmp_profile"

mv "$tmp_profile" "$profile"
trap - EXIT
