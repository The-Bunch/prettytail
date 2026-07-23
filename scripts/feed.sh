#!/usr/bin/env bash
# scripts/feed.sh <target-file>
#
# Demo feeder: appends one plausible-fake markdown fragment to
# <target-file> every 2 seconds, forever, rotating through a fixed set of
# fragment kinds (heading, paragraph, bullet list, fenced go code, table row
# batch, blockquote). Used by the demo walkthrough. Ctrl-C / TERM stop it
# cleanly.
set -euo pipefail

target="${1:?usage: feed.sh <target-file>}"

touch "$target"

n=0
running=1
trap 'running=0' INT TERM

fragment_heading() {
	printf '\n## Update %d\n\n' "$n"
}

fragment_paragraph() {
	printf '\nThe widget service is still humming along at update %d, nothing to report.\n\n' "$n"
}

fragment_bullets() {
	printf '\n- item %d.a is fine\n- item %d.b is fine\n- item %d.c needs a look\n\n' "$n" "$n" "$n"
}

fragment_code() {
	printf '\n```go\nfunc updateN%d() int {\n\treturn %d\n}\n```\n\n' "$n" "$n"
}

fragment_table() {
	printf '\n| Update | Status |\n|--------|--------|\n| %d | ok |\n\n' "$n"
}

fragment_quote() {
	printf '\n> Update %d: all quiet on the widget front.\n\n' "$n"
}

while [ "$running" -eq 1 ]; do
	case $((n % 6)) in
	0) fragment_heading >>"$target" ;;
	1) fragment_paragraph >>"$target" ;;
	2) fragment_bullets >>"$target" ;;
	3) fragment_code >>"$target" ;;
	4) fragment_table >>"$target" ;;
	5) fragment_quote >>"$target" ;;
	esac
	n=$((n + 1))
	sleep 2
done
