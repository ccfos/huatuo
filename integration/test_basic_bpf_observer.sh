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

# Verify the observer's external process/FD contract with real kernel resources.
# Workloads create objects; this script owns identity, lifetime and error assertions.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_bpf.sh"

readonly BPF_OBSERVER_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/observer"

command -v gcc > /dev/null || skip "gcc command is not installed"
command -v bpftool > /dev/null || skip "bpftool command is not installed"
command -v jq > /dev/null || skip "jq command is not installed"
command -v timeout > /dev/null || skip "timeout command is not installed"

bpf_observer_pid=""
bpf_observer_case=""
bpf_observer_pin_dir=""
bpf_observer_pin_dirs=()

cleanup() {
	local directory
	if [[ -n "${bpf_observer_pid}" ]] && jobs -pr | grep -Fxq "${bpf_observer_pid}"; then
		stop_by_pid "${bpf_observer_pid}" 0 || true
		wait "${bpf_observer_pid}" 2> /dev/null || true
	fi
	for directory in "${bpf_observer_pin_dirs[@]}"; do
		bpf_remove_pin_directory "${directory}" || log_warn "cannot remove workload pins: ${directory}"
	done
}
trap cleanup EXIT

bpf_observer_start() {
	local mode=$1
	shift
	bpf_observer_case="${BPF_OBSERVER_DIR}/${mode}"
	mkdir -p "${bpf_observer_case}"
	bpf_observer_pin_dir=$(bpf_create_pin_directory) || fatal "cannot allocate BPF workload pins"
	bpf_observer_pin_dirs+=("${bpf_observer_pin_dir}")
	"${BPF_OBSERVER_DIR}/observer" "${bpf_observer_case}" -- \
		"${BPF_OBSERVER_DIR}/workload" "${mode}" "${bpf_observer_pin_dir}" "$@" \
		> "${bpf_observer_case}/stdout.log" 2> "${bpf_observer_case}/stderr.log" &
	bpf_observer_pid=$!
}

assert_bpf_observer_completed() {
	local status=0
	wait_until 20 0.05 process_exited "${bpf_observer_pid}" \
		|| fatal "BPF observer did not complete: ${bpf_observer_case}"
	wait "${bpf_observer_pid}" || status=$?
	bpf_observer_pid=""
	[[ ${status} == 0 ]] || fatal "BPF observer/workload exited ${status}; see ${bpf_observer_case}/stderr.log"
	jq -e '.complete == true and .exit_status == 0' \
		"${bpf_observer_case}/complete.json" > /dev/null \
		|| fatal "expected complete BPF observations; actual: ${bpf_observer_case}/complete.json"
	jq -e -s --slurpfile completion "${bpf_observer_case}/complete.json" \
		'length == $completion[0].record_count' "${bpf_observer_case}/resources.jsonl" > /dev/null \
		|| fatal "BPF observer completion count does not match its journal"
}

assert_bpf_observer_pinned() {
	local kind=$1 pin_name=${2:-$1} id
	bpf_query_pinned "${kind}" "${bpf_observer_pin_dir}/${pin_name}" \
		"${bpf_observer_case}/kernel.json"
	id=$(jq -er '.id | select(type == "number" and . > 0)' "${bpf_observer_case}/kernel.json")
	jq -e -s --arg kind "${kind}" --argjson id "${id}" \
		--arg path "${bpf_observer_pin_dir}/${pin_name}" '
		any(.[]; .kind == $kind and .id == $id and .pin? == $path)
		' "${bpf_observer_case}/resources.jsonl" > /dev/null \
		|| fatal "expected BPF ${kind} ID ${id} and pin ${pin_name} in observations"
	log_info "observed pinned BPF ${kind} ID ${id}: ${pin_name}"
}

assert_bpf_observer_created() {
	local kind=$1
	jq -e -s --arg kind "${kind}" 'any(.[]; .kind == $kind and .created)' \
		"${bpf_observer_case}/resources.jsonl" > /dev/null \
		|| fatal "expected creation identity before FD close"
}

assert_bpf_observer_attachment() {
	if "${bpf_link_enumeration_available}"; then
		assert_bpf_observer_created link
	else
		jq -e -s 'any(.[]; .kind == "prog" and .legacy_attachment? == true)' \
			"${bpf_observer_case}/resources.jsonl" > /dev/null \
			|| fatal "expected legacy raw attachment program identity"
	fi
}

assert_bpf_observer_map_count() {
	jq -e -s '[.[] | select(.created and .kind == "map") | .id] |
			length == 100 and (unique | length) == 100' \
		"${bpf_observer_case}/resources.jsonl" > /dev/null \
		|| fatal "expected 100 map IDs during ${bpf_observer_mode}"
}

assert_bpf_observer_descendant() {
	jq -e -s --argjson root "$(jq .root_pid "${bpf_observer_case}/complete.json")" \
		'any(.[]; .created and .pid != $root)' "${bpf_observer_case}/resources.jsonl" > /dev/null \
		|| fatal "expected resource identity from the reparented child"
}

assert_bpf_observer_sigterm() {
	jq -e '.sigterm_forwarded == true' "${bpf_observer_case}/complete.json" > /dev/null \
		|| fatal "expected SIGTERM delivery before late resource creation"
}

assert_bpf_observer_borrowed() {
	jq -e -s 'all(.[]; .created == false)' "${bpf_observer_case}/resources.jsonl" > /dev/null \
		|| fatal "borrowed map must not be reported as newly created"
}

assert_bpf_observer_rejected() {
	local status
	wait_until 5 0.05 process_exited "${bpf_observer_pid}" || fatal "unsupported operation was not rejected"
	status=0
	wait "${bpf_observer_pid}" || status=$?
	bpf_observer_pid=""
	[[ ${status} != 0 ]] || fatal "unsupported observation unexpectedly succeeded"
	jq -e '.complete == false' "${bpf_observer_case}/complete.json" > /dev/null \
		|| fatal "unsupported operation must invalidate observation completeness"
}

bpf_build_observer_tools "${BPF_OBSERVER_DIR}"
bpf_probe_link_enumeration "${BPF_OBSERVER_DIR}"

# Workloads must raise their own limit even when the parent starts with little headroom.
ulimit -S -l 64 || fatal "cannot set the observer workload memlock precondition"

for bpf_observer_kind in map prog; do
	bpf_observer_start "${bpf_observer_kind}"
	assert_bpf_observer_completed
	assert_bpf_observer_pinned "${bpf_observer_kind}"
	assert_bpf_observer_created "${bpf_observer_kind}"
done

if "${bpf_link_enumeration_available}"; then
	bpf_observer_start link
	assert_bpf_observer_completed
	assert_bpf_observer_pinned link
	assert_bpf_observer_created link
else
	log_info "kernel has no link enumeration; exercising legacy raw attachment FD lifetime"
fi
bpf_observer_start raw
assert_bpf_observer_completed
assert_bpf_observer_created prog
assert_bpf_observer_attachment

for bpf_observer_mode in race fork_race; do
	bpf_observer_start "${bpf_observer_mode}"
	assert_bpf_observer_completed
	assert_bpf_observer_map_count
	log_info "observed all 100 map creations during ${bpf_observer_mode}"
done

bpf_observer_start orphan
assert_bpf_observer_completed
assert_bpf_observer_pinned map
assert_bpf_observer_descendant

bpf_observer_start late "${BPF_OBSERVER_DIR}/late-ready"
wait_until 5 0.05 test -e "${BPF_OBSERVER_DIR}/late-ready" || fatal "late workload did not become ready"
kill -TERM "${bpf_observer_pid}"
assert_bpf_observer_completed
assert_bpf_observer_pinned map
assert_bpf_observer_sigterm

bpf_observer_borrowed_source=$(bpf_create_pin_directory) || fatal "cannot allocate baseline BPF pin"
bpf_observer_pin_dirs+=("${bpf_observer_borrowed_source}")
"${BPF_OBSERVER_DIR}/workload" map "${bpf_observer_borrowed_source}"
bpf_observer_start borrowed "${bpf_observer_borrowed_source}/map"
assert_bpf_observer_completed
assert_bpf_observer_pinned map borrowed
assert_bpf_observer_borrowed

bpf_observer_start unsupported
assert_bpf_observer_rejected
log_info "BPF observer recorded short-lived, reparented and shutdown resources; incomplete coverage failed"
