#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

usage() {
	cat << 'HELP'
Usage: bash integration/apiserver/run.sh [functional|load|soak]

Requires Linux, root, make build output, Docker, curl, jq, Go and Python 3.
functional (default): capabilities, quotas, invalid requests and Node failures.
load: 100 concurrent query clients for 600 seconds (70% list, 30% detail).
soak: repeated profiling and queries for 86400 seconds.

Environment:
  APISERVER_TEST_OUTPUT       Report directory (default: new /tmp directory)
  APISERVER_TEST_SECONDS      Override load/soak duration
  APISERVER_TEST_CLIENTS      Query clients (default 100)
  APISERVER_TEST_RPS          Aggregate query rate (default 100)

Missing prerequisites exit 77, assertion failures exit 1. Reports are retained.
F02/F03/F04/F08/F13 and the Kubernetes matrix run through e2e/run.sh.
Functional cases also run via integration/run.sh test_apiserver_profiling.sh.
Load and soak remain explicit; they are not included in make integration/e2e.
HELP
}
if [[ ${1:-} == --help || ${1:-} == -h ]]; then
	usage
	exit 0
fi
mode=${1:-functional}
[[ $# -le 1 ]] || {
	usage >&2
	exit 2
}
case "$mode" in functional | load | soak) ;; *)
	usage >&2
	exit 2
	;;
esac
[[ $(uname -s) == Linux && ${EUID} -eq 0 ]] || {
	echo 'SKIP: Linux and root are required' >&2
	exit 77
}
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
for command in unshare mount flock pgrep; do command -v "$command" > /dev/null || {
	echo "SKIP: missing $command" >&2
	exit 77
}; done
# bamai uses a fixed toolstream socket, so even separate ports cannot isolate peers.
exec 9> /var/lock/huatuo-apiserver-standalone.lock
flock -n 9 || {
	echo 'another standalone apiserver test is running' >&2
	exit 1
}
if [[ -S /var/run/huatuo-toolstream.sock ]] || pgrep -x huatuo-bamai > /dev/null; then
	echo 'stop the existing huatuo-bamai before running this suite' >&2
	exit 1
fi
output=${APISERVER_TEST_OUTPUT:-$(mktemp -d /tmp/huatuo-apiserver.XXXXXX)}
mkdir -p "$output"
output=$(cd "$output" && pwd)
[[ ! -e "$output/summary.tsv" ]] || {
	echo "report directory already used: $output" >&2
	exit 2
}
export APISERVER_TEST_OUTPUT="$output"
printf 'Reports: %s\n' "$output"
unshare --uts --mount bash -c '
	set -euo pipefail
	mount --make-rprivate /
	echo huatuo-dev > /proc/sys/kernel/hostname
	cd "$1"
	source integration/env.sh
	rmdir "$HUATUO_BAMAI_TEST_TMPDIR"
	export HUATUO_BAMAI_TEST_TMPDIR="$APISERVER_TEST_OUTPUT"
	exec bash integration/apiserver/cases.sh "$2"
' _ "$root" "$mode"
