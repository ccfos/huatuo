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

# bpftool I/O and compatibility. Source lib.sh first for diagnostics and kernel checks.
# Optional deadlines are absolute epoch seconds; zero uses a 10-second query budget.

set -euo pipefail

if [[ -n "${__HUATUO_LIB_BPF_SH_LOADED:-}" ]]; then
	return 0
fi
readonly __HUATUO_LIB_BPF_SH_LOADED=1

bpf_link_enumeration_available=false

bpf_run_query() {
	local destination=$1 deadline=$2
	shift 2
	local timeout_seconds=10 status=0
	if ((deadline > 0)); then
		timeout_seconds=$((deadline - $(date +%s)))
		((timeout_seconds > 0)) || fatal "BPF query deadline expired before bpftool $*"
	fi
	LC_ALL=C timeout --signal=KILL "${timeout_seconds}s" bpftool "$@" \
		> "${destination}" 2> "${destination}.stderr.log" || status=$?
	if [[ ${status} -eq 124 || ${status} -eq 137 ]]; then
		fatal "bpftool $* timed out after ${timeout_seconds}s; stdout: ${destination}; stderr: ${destination}.stderr.log"
	fi
	if ((deadline > 0 && $(date +%s) >= deadline)); then
		fatal "BPF query deadline exceeded by bpftool $*; stdout: ${destination}; stderr: ${destination}.stderr.log"
	fi
	return "${status}"
}

bpf_query() {
	local kind=$1 destination=$2 deadline=${3:-0} attempt
	local options=(-j)
	if [[ "${kind}" == perf ]]; then
		# Retries share the caller's deadline or one default query budget.
		((deadline > 0)) || deadline=$(($(date +%s) + 10))
	else
		options+=(-f)
	fi
	for attempt in 1 2 3; do
		if bpf_run_query "${destination}" "${deadline}" "${options[@]}" "${kind}" show; then
			break
		fi
		# Old bpftool can abort its /proc walk when a process exits. Discard that scan.
		if [[ "${kind}" != perf || ${attempt} -eq 3 || -s "${destination}.stderr.log" ]] \
			|| ! jq -e -s 'length == 1 and (.[0] | type == "array" and
				[.[] | select(has("error")) | .error] == ["No such process"])' \
				"${destination}" > /dev/null 2>&1; then
			fatal "bpftool ${kind} show failed after ${attempt} attempt(s); stdout: ${destination}; stderr: ${destination}.stderr.log"
		fi
		log_info "bpftool perf scan interrupted by a process exit; retrying ($((attempt + 1))/3)"
	done
	jq -e -s 'length == 1 and (.[0] | type == "array" and all(.[]; type == "object"))' "${destination}" > /dev/null \
		|| fatal "invalid bpftool ${kind} JSON: ${destination}"
	if [[ "${kind}" == perf ]]; then
		jq -e 'all(.[]; (.pid | type) == "number" and .pid > 0
			and (.prog_id | type) == "number" and .prog_id > 0)' "${destination}" > /dev/null
	else
		jq -e 'all(.[]; (.id | type) == "number" and .id > 0)' "${destination}" > /dev/null
	fi || fatal "missing IDs in bpftool ${kind} JSON: ${destination}"
}

bpf_query_by_id() {
	local kind=$1 id=$2 destination=$3 deadline=${4:-0} missing_error status=0
	# Some bpftool releases fail successful link specifier queries. A validated
	# listing filtered by the exact ID avoids their ambiguous exit status.
	if [[ "${kind}" == link ]]; then
		bpf_query link "${destination}.links.json" "${deadline}"
		jq -e --argjson id "${id}" 'map(select(.id == $id)) |
			if length == 0 then empty elif length == 1 then .[0]
			else error("duplicate BPF link ID") end' \
			"${destination}.links.json" > "${destination}" || status=$?
		case ${status} in
		0) return 0 ;;
		4) return 1 ;;
		*) fatal "cannot select BPF link ID ${id}: ${destination}.links.json" ;;
		esac
	fi
	case "${kind}" in
	prog) missing_error="get by id (${id}): No such file or directory" ;;
	map) missing_error="get map by id (${id}): No such file or directory" ;;
	*) fatal "cannot query BPF resource kind by ID: ${kind}" ;;
	esac

	if bpf_run_query "${destination}" "${deadline}" -j -f "${kind}" show id "${id}"; then
		jq -e -s --argjson id "${id}" 'length == 1 and (.[0] | type == "object" and .id == $id)' \
			"${destination}" > /dev/null \
			|| fatal "invalid bpftool ${kind} ID ${id} JSON: ${destination}"
		return 0
	fi

	# Only ENOENT for this ID proves reclamation; other query failures must fail the test.
	if jq -e -s --arg error "${missing_error}" 'length == 1 and (.[0] | type == "object" and .error == $error)' \
		"${destination}" > /dev/null; then
		return 1
	fi
	fatal "bpftool ${kind} show id ${id} failed; stdout: ${destination}; stderr: ${destination}.stderr.log"
}

bpf_query_pinned() {
	local kind=$1 path=$2 destination=$3
	bpf_query "${kind}" "${destination}.objects.json"
	jq -e --arg path "${path}" '[.[] | select(any(.pinned[]?; . == $path))] |
		if length == 1 then .[0] else error("expected one pinned BPF object") end' \
		"${destination}.objects.json" > "${destination}" \
		|| fatal "expected one BPF ${kind} pinned at ${path}; see ${destination}.objects.json"
}

bpf_snapshot() {
	local directory=$1 deadline=${2:-0} kind
	mkdir -p "${directory}" || fatal "cannot create ${directory} snapshot directory"
	for kind in prog map perf; do
		bpf_query "${kind}" "${directory}/${kind}.json" "${deadline}"
	done
	if "${bpf_link_enumeration_available}"; then
		bpf_query link "${directory}/link.json" "${deadline}"
	else
		echo '[]' > "${directory}/link.json" || fatal "cannot record unavailable BPF links"
	fi
	bpf_write_snapshot "${directory}"
}

bpf_write_snapshot() {
	local directory=$1
	jq -n --slurpfile prog "${directory}/prog.json" \
		--slurpfile map "${directory}/map.json" \
		--slurpfile link "${directory}/link.json" \
		--slurpfile perf "${directory}/perf.json" \
		'{prog: $prog[0], map: $map[0], link: $link[0], perf: $perf[0]}' \
		> "${directory}.json" || fatal "cannot assemble ${directory} BPF snapshot"
}

bpf_probe_link_enumeration() {
	local directory=$1 link_error
	bpf_link_enumeration_available=false
	# Enumeration arrived in 5.8. Older kernels use perf/raw tracepoint FDs; on
	# newer kernels a failed query must not silently weaken the no-leak assertion.
	if bpf_run_query "${directory}/link-probe.json" 0 -j -f link show; then
		jq -e -s 'length == 1 and (.[0] | type == "array")' "${directory}/link-probe.json" > /dev/null \
			|| fatal "invalid bpftool link JSON"
		bpf_link_enumeration_available=true
	else
		# JSON mode can report errors on stdout while leaving stderr empty.
		jq -r '.. | objects | .error? | select(type == "string")' \
			"${directory}/link-probe.json" > "${directory}/link-probe-errors.txt" \
			|| fatal "invalid bpftool link JSON: ${directory}/link-probe.json"
		cat "${directory}/link-probe.json.stderr.log" >> "${directory}/link-probe-errors.txt"
		link_error=$(< "${directory}/link-probe-errors.txt")
		if ! kernel_version_le 5 7; then
			fatal "cannot enumerate BPF links: ${link_error}; see ${directory}/link-probe.json{,.stderr.log}"
		fi
		if [[ -n "${link_error}" ]]; then
			if grep -Eq 'Permission denied|Operation not permitted' "${directory}/link-probe-errors.txt" \
				|| ! grep -Eq 'Invalid argument|Operation not supported|Function not implemented' "${directory}/link-probe-errors.txt"; then
				fatal "bpftool link query failed: ${link_error}; see ${directory}/link-probe.json{,.stderr.log}"
			fi
		else
			bpf_run_query "${directory}/link-help.txt" 0 link help || true
			if grep -q 'bpftool link' "${directory}/link-help.txt" "${directory}/link-help.txt.stderr.log"; then
				fatal "bpftool link query failed without an error; see ${directory}/link-probe.json{,.stderr.log}"
			fi
		fi
		log_info "BPF link enumeration unavailable; legacy attachments require perf queries"
	fi
}

bpf_build_observer_tools() {
	local directory=$1
	mkdir -p "${directory}" || fatal "cannot create BPF observer workspace"
	compile_user_fixture "${ROOT_DIR}/integration/testdata/tools/test_bpf_observer.c" \
		"${directory}/observer" -std=gnu11 -O2 -Werror
	compile_user_fixture "${ROOT_DIR}/integration/testdata/tools/test_bpf_resource_workload.c" \
		"${directory}/workload" -std=gnu11 -O2 -Werror -pthread
}

bpf_create_pin_directory() {
	# The runner's private mount namespace contains any temporary bpffs mount.
	if [[ $(stat -f -c %t /sys/fs/bpf) != cafe4a11 ]]; then
		mount -t bpf bpf /sys/fs/bpf || fatal "cannot mount bpffs in the test namespace"
	fi
	mktemp -d /sys/fs/bpf/huatuo_test_XXXXXX
}

bpf_remove_pin_directory() {
	local directory=${1:-}
	[[ -n "${directory}" && -d "${directory}" ]] || return 0
	if [[ ${directory%/*} != /sys/fs/bpf || ${directory##*/} != huatuo_test_* ]]; then
		log_error "refusing to remove BPF pins outside the allocated test directory: ${directory}"
		return 1
	fi
	rm -f "${directory}/map" "${directory}/prog" "${directory}/link" "${directory}/borrowed" || return 1
	rmdir "${directory}"
}
