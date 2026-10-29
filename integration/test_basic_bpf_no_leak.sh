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

readonly BPF_LEAK_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/bpf"
readonly BPF_LEAK_PID_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
readonly BPF_LEAK_RECLAIM_TIMEOUT=20
readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)

require_commands bpftool jq timeout awk curl

bpf_leak_kinds=(prog map perf)
bpf_leak_deadline=0
trap huatuo_bamai_stop EXIT

bpf_leak_query() {
	local kind=$1 destination=$2 attempt budget
	for attempt in 1 2 3; do
		budget=10
		if ((bpf_leak_deadline > 0)); then
			budget=$((bpf_leak_deadline - SECONDS))
			((budget > 0)) || fatal "BPF reclamation deadline exceeded; see ${BPF_LEAK_DIR}"
			((budget <= 10)) || budget=10
		fi
		if LC_ALL=C timeout --signal=KILL "${budget}s" bpftool -j "${kind}" show \
			> "${destination}" 2> "${destination}.stderr"; then
			break
		fi
		# Old bpftool may lose a process while scanning perf FDs. Retry that scan only.
		if [[ ${kind} != perf || ${attempt} == 3 || -s "${destination}.stderr" ]] || ! jq -e \
			'type == "array" and [.[] | .error? // empty] == ["No such process"]' \
			"${destination}" > /dev/null 2>&1; then
			fatal "bpftool ${kind} show failed (attempt ${attempt}); see ${destination}{,.stderr}"
		fi
	done
	jq -e --arg kind "${kind}" 'type == "array" and all(.[];
		if $kind == "perf" then
			(.pid | type == "number" and . > 0) and (.prog_id | type == "number" and . > 0)
		else (.id | type == "number" and . > 0) end)' "${destination}" > /dev/null \
		|| fatal "invalid bpftool ${kind} JSON: ${destination}"
}

bpf_leak_snapshot() {
	local phase=$1 kind directory="${BPF_LEAK_DIR}/$1"
	mkdir -p "${directory}" || fatal "cannot create BPF snapshot directory: ${directory}"
	printf '[]\n' > "${directory}/link.json"
	for kind in "${bpf_leak_kinds[@]}"; do
		bpf_leak_query "${kind}" "${directory}/${kind}.json"
	done
	jq -n --slurpfile prog "${directory}/prog.json" --slurpfile map "${directory}/map.json" \
		--slurpfile link "${directory}/link.json" --slurpfile perf "${directory}/perf.json" \
		'{prog: $prog[0], map: $map[0], link: $link[0], perf: $perf[0]}' \
		> "${BPF_LEAK_DIR}/${phase}.json" || fatal "cannot assemble BPF ${phase} snapshot"
}

bpf_leak_resources_loaded() {
	local pid
	pid=$(< "${BPF_LEAK_PID_FILE}")
	process_exited "${pid}" && fatal "bamai exited before BPF resources could be recorded"
	# A descriptor may close during the walk; retry rather than accept a partial inventory.
	awk '$1 ~ /^(prog|map|link)_id:$/ {sub(/_id:$/, "", $1); print $1, $2}' \
		/proc/"${pid}"/fdinfo/* > "${BPF_LEAK_DIR}/fd-ids.txt" \
		2> "${BPF_LEAK_DIR}/fdinfo.stderr" || return 1
	jq -Rn '[inputs | split(" ") | {kind: .[0], id: (.[1] | tonumber)}]' \
		< "${BPF_LEAK_DIR}/fd-ids.txt" > "${BPF_LEAK_DIR}/fd-ids.json" \
		|| fatal "cannot parse bamai BPF FD IDs"
	bpf_leak_snapshot before
	jq -n --slurpfile fds "${BPF_LEAK_DIR}/fd-ids.json" \
		--slurpfile before "${BPF_LEAK_DIR}/before.json" '
		def fd_ids($kind): [$fds[0][] | select(.kind == $kind) | .id];
		fd_ids("prog") as $programs |
		{prog: ($programs | unique),
		 map: ((fd_ids("map") + [$before[0].prog[] |
			select(.id as $id | $programs | index($id)) | .map_ids[]?]) | unique),
		 link: ([$before[0].link[] | select(
			(.id as $id | fd_ids("link") | index($id)) or
			(.prog_id as $id | $programs | index($id))) | .id] | unique)}' \
		> "${BPF_LEAK_DIR}/owned.json" || fatal "cannot record bamai BPF ownership"
	jq -e --argjson pid "${pid}" --slurpfile owned "${BPF_LEAK_DIR}/owned.json" '
		$owned[0] as $owned |
		($owned.prog | length) > 0 and ($owned.map | length) > 0 and
		(($owned.link | length) > 0 or any(.perf[];
			.pid == $pid and (.prog_id as $id | $owned.prog | index($id))))' \
		"${BPF_LEAK_DIR}/before.json" > /dev/null
}

bpf_leak_resources_reclaimed() {
	bpf_leak_snapshot after
	jq -n --slurpfile owned "${BPF_LEAK_DIR}/owned.json" \
		--slurpfile after "${BPF_LEAK_DIR}/after.json" '
		$owned[0] as $owned | $after[0] as $after |
		def surviving($kind): [$after[$kind][] | select(.id as $id | $owned[$kind] | index($id))];
		{prog: surviving("prog"), map: surviving("map"), link: surviving("link"),
		 perf: [$after.perf[] | select(.prog_id as $id | $owned.prog | index($id))]}' \
		> "${BPF_LEAK_DIR}/remaining.json" || fatal "cannot compare bamai BPF IDs"
	((SECONDS < bpf_leak_deadline)) || fatal "BPF reclamation exceeded its ${BPF_LEAK_RECLAIM_TIMEOUT}-second window"
	jq -e 'all(.[]; length == 0)' "${BPF_LEAK_DIR}/remaining.json" > /dev/null
}

assert_bpf_loaded() {
	wait_until "${WAIT_HUATUO_BAMAI_TIMEOUT}" 1 bpf_leak_resources_loaded \
		|| fatal "expected loaded and attached bamai BPF; see ${BPF_LEAK_DIR}"
	log_info "bamai BPF IDs before SIGTERM: $(jq -c . "${BPF_LEAK_DIR}/owned.json")"
}

assert_bpf_reclaimed() {
	bpf_leak_deadline=$((SECONDS + BPF_LEAK_RECLAIM_TIMEOUT))
	wait_until "${BPF_LEAK_RECLAIM_TIMEOUT}" 1 bpf_leak_resources_reclaimed \
		|| fatal "expected recorded BPF IDs to disappear; remaining: $(< "${BPF_LEAK_DIR}/remaining.json")"
	log_info "all recorded bamai BPF IDs disappeared after SIGTERM"
}

mkdir -p "${BPF_LEAK_DIR}"
if kernel_version_le 5 7; then
	log_info "BPF link enumeration is not checked on kernels older than 5.8"
else
	bpf_leak_kinds+=(link)
fi
integration_huatuo_bamai_start
assert_bpf_loaded
assert_huatuo_bamai_shutdown_with_signal TERM
assert_bpf_reclaimed
