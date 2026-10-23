#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

usage() {
	cat << 'HELP'
Usage: bash integration/apiserver/run.sh

Requires Linux, root, make build test-build output, Docker, curl, jq and Python 3.
Checks quotas, invalid requests and Node failures.

Environment:
  APISERVER_TEST_OUTPUT       Report directory (default: new /tmp directory)

Missing prerequisites exit 77, assertion failures exit 1. Reports are retained.
F02/F03/F04/F08/F13 and the Kubernetes matrix run through e2e/run.sh.
Functional cases also run via integration/run.sh test_apiserver_profiling.sh.
HELP
}
if [[ ${1:-} == --help || ${1:-} == -h ]]; then
	usage
	exit 0
fi
[[ $# -eq 0 ]] || {
	usage >&2
	exit 2
}
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
	exec bash integration/apiserver/cases.sh
' _ "$root"
