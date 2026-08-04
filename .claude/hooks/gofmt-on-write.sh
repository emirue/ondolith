#!/bin/sh
# gofmt any .go file right after it is written, so formatting never shows up as
# a `make check` failure two steps later.
#
# Reads the PostToolUse hook payload on stdin. Exits 0 no matter what: a
# formatting helper must never surface as a hook error, and only exit code 2
# would block, which is not wanted here either.

file=$(jq -r '.tool_input.file_path // empty' 2>/dev/null) || exit 0

case "$file" in
	*.go) ;;
	*) exit 0 ;;
esac

[ -f "$file" ] || exit 0
# `--` so a path beginning with '-' is never read as an option.
gofmt -w -- "$file" 2>/dev/null || exit 0
exit 0
