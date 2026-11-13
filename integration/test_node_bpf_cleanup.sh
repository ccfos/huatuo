#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Check BPF objects observed through bamai's FDs before SIGTERM are reclaimed.
# Pins, descendants and objects created outside this snapshot are out of scope.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly BPF_CLEANUP_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/bpf"
readonly BPF_CLEANUP_PID_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
readonly BPF_CLEANUP_RECLAIM_TIMEOUT=20

require_commands bpftool jq timeout awk curl
# Distribution wrappers may exist without an installed bpftool binary.
if ! timeout --signal=KILL 10s bpftool version > "${HUATUO_BAMAI_TEST_TMPDIR}/bpftool-version.log" 2>&1; then
	skip "bpftool is unavailable; install a working bpftool: $(< "${HUATUO_BAMAI_TEST_TMPDIR}/bpftool-version.log")"
fi

bpf_cleanup_check_links=false
bpf_cleanup_deadline=0
trap huatuo_bamai_stop EXIT

# Return 0 for validated output, 1 for a retryable perf scan race; other failures exit.
bpf_cleanup_query() {
	local kind=$1 destination=$2 budget=10 status
	if ((bpf_cleanup_deadline > 0)); then
		budget=$((bpf_cleanup_deadline - SECONDS))
		((budget > 0)) || fatal "BPF reclamation deadline exceeded; see ${BPF_CLEANUP_DIR}"
		((budget <= 10)) || budget=10
	fi
	if LC_ALL=C timeout --signal=KILL "${budget}s" bpftool -j "${kind}" show \
		> "${destination}" 2> "${destination}.err"; then
		jq -e --arg kind "${kind}" 'type == "array" and all(.[];
			if $kind == "perf" then
				(.pid | type == "number" and . > 0) and (.prog_id | type == "number" and . > 0)
			else (.id | type == "number" and . > 0) end)' "${destination}" > /dev/null \
			|| fatal "invalid bpftool ${kind} JSON: ${destination}"
		return 0
	else
		status=$?
	fi
	# Retry bpftool failures (including -1 as 255), not timeout or signal exits.
	if [[ ${kind} == perf && (${status} == 1 || ${status} == 255) && ! -s "${destination}.err" ]] && jq -e \
		'type == "array" and [.[] | .error? // empty] == ["No such process"]' \
		"${destination}" > /dev/null 2>&1; then
		return 1
	fi
	fatal "bpftool ${kind} show failed: exit=${status}, budget=${budget}s; see ${destination}{,.err}"
}

bpf_cleanup_record_owned() {
	local pid=$1
	# A descriptor may close during the walk; retry rather than accept a partial inventory.
	awk '$1 ~ /^(prog|map|link)_id:$/ {sub(/_id:$/, "", $1); print $1, $2}' \
		/proc/"${pid}"/fdinfo/* > "${BPF_CLEANUP_DIR}/fd-ids.txt" \
		2> "${BPF_CLEANUP_DIR}/fdinfo.err" || return 1
	jq -Rn 'reduce (inputs | split(" ")) as $fd
		({prog: [], map: [], link: []}; .[$fd[0]] += [($fd[1] | tonumber)]) |
		map_values(unique)' < "${BPF_CLEANUP_DIR}/fd-ids.txt" \
		> "${BPF_CLEANUP_DIR}/fd-ids.json" || fatal "cannot parse bamai BPF FD IDs"
	bpf_cleanup_query prog "${BPF_CLEANUP_DIR}/before/prog.json" || return 1
	if [[ ${bpf_cleanup_check_links} == true ]]; then
		bpf_cleanup_query link "${BPF_CLEANUP_DIR}/before/link.json" || return 1
	fi
	jq -n --slurpfile fds "${BPF_CLEANUP_DIR}/fd-ids.json" \
		--slurpfile prog "${BPF_CLEANUP_DIR}/before/prog.json" \
		--slurpfile link "${BPF_CLEANUP_DIR}/before/link.json" '
		def contains_id($ids; $id): ($ids | index($id)) != null;
		$fds[0] as $fds |
		{prog: $fds.prog,
		 map: (($fds.map + [$prog[0][] |
			select(contains_id($fds.prog; .id)) | .map_ids[]?]) | unique),
		 link: ([$link[0][] | select(
			contains_id($fds.link; .id) or contains_id($fds.prog; .prog_id)) | .id] | unique)}' \
		> "${BPF_CLEANUP_DIR}/owned.json" || fatal "cannot record bamai BPF ownership"
}

bpf_cleanup_resources_loaded() {
	local pid=$1
	process_exited "${pid}" && fatal "bamai exited before BPF resources could be recorded"

	bpf_cleanup_record_owned "${pid}" || return 1

	jq -e '(.prog | length) > 0 and (.map | length) > 0' \
		"${BPF_CLEANUP_DIR}/owned.json" > /dev/null || return 1
	if jq -e '(.link | length) > 0' "${BPF_CLEANUP_DIR}/owned.json" > /dev/null; then
		return 0
	fi
	bpf_cleanup_query perf "${BPF_CLEANUP_DIR}/before/perf.json" || return 1
	jq -e --argjson pid "${pid}" --slurpfile owned "${BPF_CLEANUP_DIR}/owned.json" '
		any(.[]; .pid == $pid and (.prog_id as $id | $owned[0].prog | index($id)))' \
		"${BPF_CLEANUP_DIR}/before/perf.json" > /dev/null
}

bpf_cleanup_resources_reclaimed() {
	local kind
	for kind in prog map; do
		bpf_cleanup_query "${kind}" "${BPF_CLEANUP_DIR}/after/${kind}.json" || return 1
	done
	if [[ ${bpf_cleanup_check_links} == true ]]; then
		bpf_cleanup_query link "${BPF_CLEANUP_DIR}/after/link.json" || return 1
	fi
	# A surviving perf attachment would keep its recorded program ID alive.
	jq -n --slurpfile owned "${BPF_CLEANUP_DIR}/owned.json" \
		--slurpfile prog "${BPF_CLEANUP_DIR}/after/prog.json" \
		--slurpfile map "${BPF_CLEANUP_DIR}/after/map.json" \
		--slurpfile link "${BPF_CLEANUP_DIR}/after/link.json" '
		$owned[0] as $owned |
		def surviving($objects; $ids): [$objects[] | select(.id as $id | $ids | index($id))];
		{prog: surviving($prog[0]; $owned.prog), map: surviving($map[0]; $owned.map),
		 link: surviving($link[0]; $owned.link)}' \
		> "${BPF_CLEANUP_DIR}/remaining.json" || fatal "cannot compare bamai BPF IDs"
	((SECONDS < bpf_cleanup_deadline)) || fatal "BPF reclamation exceeded its ${BPF_CLEANUP_RECLAIM_TIMEOUT}-second window"
	jq -e 'all(.[]; length == 0)' "${BPF_CLEANUP_DIR}/remaining.json" > /dev/null
}

assert_bpf_loaded() {
	local pid
	pid=$(< "${BPF_CLEANUP_PID_FILE}")
	wait_until "${WAIT_HUATUO_BAMAI_TIMEOUT}" 1 bpf_cleanup_resources_loaded "${pid}" \
		|| fatal "expected loaded and attached bamai BPF; see ${BPF_CLEANUP_DIR}"
	log_info "bamai BPF IDs before SIGTERM: $(jq -c . "${BPF_CLEANUP_DIR}/owned.json")"
}

assert_bpf_reclaimed() {
	bpf_cleanup_deadline=$((SECONDS + BPF_CLEANUP_RECLAIM_TIMEOUT))
	wait_until "${BPF_CLEANUP_RECLAIM_TIMEOUT}" 1 bpf_cleanup_resources_reclaimed \
		|| fatal "expected recorded BPF IDs to disappear; remaining: $(< "${BPF_CLEANUP_DIR}/remaining.json")"
	log_info "all recorded bamai BPF IDs disappeared after SIGTERM"
}

mkdir -p "${BPF_CLEANUP_DIR}/before" "${BPF_CLEANUP_DIR}/after"
if kernel_version_le 5 7; then
	log_info "BPF link enumeration is not checked on kernels older than 5.8"
	printf '[]\n' > "${BPF_CLEANUP_DIR}/before/link.json"
	printf '[]\n' > "${BPF_CLEANUP_DIR}/after/link.json"
else
	bpf_cleanup_check_links=true
fi
integration_huatuo_bamai_start
assert_bpf_loaded
assert_huatuo_bamai_shutdown_with_signal TERM
assert_bpf_reclaimed
