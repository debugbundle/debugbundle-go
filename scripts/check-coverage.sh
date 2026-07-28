#!/bin/sh
set -eu

go_binary="${GO:-go}"
minimum="${COVERAGE_MINIMUM:-80}"
profile="$(mktemp)"
report="$(mktemp)"
trap 'rm -f "$profile" "$report"' EXIT

packages="$("$go_binary" list ./... | awk '!/\/examples\//')"
# Package paths cannot contain shell whitespace. Expanding the list lets one
# coverage profile account for every publishable package while excluding the
# standalone example applications.
# shellcheck disable=SC2086
"$go_binary" test $packages -coverprofile="$profile"

awk -v minimum="$minimum" '
  NR > 1 {
    separator = index($1, ":")
    file = substr($1, 1, separator - 1)
    total[file] += $2
    if ($3 > 0) {
      covered[file] += $2
    }
  }
  END {
    failed = 0
    for (file in total) {
      percent = total[file] == 0 ? 100 : (covered[file] * 100 / total[file])
      printf "%.2f%% %s (%d/%d statements)\n", percent, file, covered[file], total[file]
      if (percent + 0.000001 < minimum) {
        failed = 1
      }
    }
    exit failed
  }
' "$profile" >"$report" || {
  sort -n "$report"
  echo "Per-file Go statement coverage must be at least ${minimum}%." >&2
  exit 1
}

sort -n "$report"
