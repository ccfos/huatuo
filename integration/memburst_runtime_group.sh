#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Controlled procfs input, real processes, collector and daemon in a private PID namespace.
set -euo pipefail
source "${ROOT_DIR}/integration/lib_memburst.sh"
require_commands go jq mount umount awk timeout readlink
mount -t tmpfs tmpfs /var/run
unset HUATUO_BAMAI_DEFAULT_ADDR
huatuo_bamai_initialize_address
burst_setup
trap burst_cleanup EXIT
assert_burst_runtime() {
	local mode=$1 language=${1} count=$2
	go_snapshot_case_dir=${HUATUO_BAMAI_TEST_TMPDIR}
	case ${mode} in go | disabled | churn) language=go ;; c) language=native ;; esac
	jq -se --argjson pid "${burst_runtime_pid}" --arg language "${language}" '
 last.tracer_data as $d |
 ($d.top_memory_usage | length > 0 and length <= 10)
 and ($d.process_snapshots | length == ([3, ($d.top_memory_usage | length)] | min))
 and all(range(0; ($d.process_snapshots | length)); . as $i |
  $d.process_snapshots[$i].pid == $d.top_memory_usage[$i].PID)
 and any($d.process_snapshots[]; .pid == $pid and .language == $language
  and .process_memory.rss_bytes >= 8388608)
 and (($d | tojson | utf8bytelength) <= 2097152)
 ' "${go_snapshot_case_dir}/events/memburst" > /dev/null || fatal "${mode}: invalid runtime identity or event budget"
	jq -s --argjson pid "${burst_runtime_pid}" \
		'{tracer_data:(last.tracer_data.process_snapshots[] | select(.pid == $pid))}' \
		"${go_snapshot_case_dir}/events/memburst" > "${go_snapshot_case_dir}/snapshot.json"
	case ${mode} in
	go | disabled) assert_go_snapshot_statistics "${mode}" ;;
	churn)
		jq -e '.tracer_data.snapshot | (.status == "complete" or .status == "partial")
 and (.entries | length > 0 and length <= 10)
 and all(.entries[]; .objects > 0 and .average_bytes == (.bytes / .objects))' \
			"${go_snapshot_case_dir}/snapshot.json" > /dev/null || fatal "invalid concurrent allocation snapshot"
		;;
	# Each memburst process has only a two-second census budget.
	python) assert_snapshot_runtime_payload python deadline-partial ;;
	*) assert_snapshot_runtime_payload "${language}" ;;
	esac
	assert_burst_count "${count}"
	assert_memsnapshot_tracing_metrics memburst integration 1
}

assert_burst_storage_failure() {
	grep -F 'msg="save memburst snapshot"' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" | grep -Fq 'not a directory' || fatal "storage failure diagnostic missing expected cause"
}

for memburst_runtime in "$@"; do
	log_info "START runtime ${memburst_runtime}"
	case ${memburst_runtime} in
	python) command -v python3 > /dev/null || {
		log_info 'SKIP: Python runtime unavailable'
		continue
	} ;;
	java) [[ -f ${MEMBURST_RUNTIME_DIR}/HeapFixture.class ]] || {
		log_info 'SKIP: Java runtime unavailable'
		continue
	} ;;
	c | cpp) [[ -x ${MEMBURST_RUNTIME_DIR}/${memburst_runtime}-snapshot ]] || {
		log_info "SKIP: ${memburst_runtime} compiler unavailable"
		continue
	} ;;
	esac
	HUATUO_BAMAI_TEST_TMPDIR="${MEMBURST_ROOT_WORKSPACE}/runtime-${memburst_runtime}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	burst_start_runtime "${memburst_runtime}"
	burst_top=10
	burst_threshold=10
	burst_anon=0
	burst_blacklist=0
	burst_cooldown=1
	burst_input low
	burst_start
	if [[ ${memburst_runtime} == go ]]; then
		if [[ -d ${HUATUO_BAMAI_TEST_TMPDIR}/events ]]; then rmdir "${HUATUO_BAMAI_TEST_TMPDIR}/events"; fi
		: > "${HUATUO_BAMAI_TEST_TMPDIR}/events"
		burst_wait_samples low
		burst_input high
		wait_until 30 .1 grep -Fq 'msg="save memburst snapshot"' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
			|| fatal "storage obstruction did not produce a diagnostic"
		assert_burst_storage_failure
		touch "${HUATUO_BAMAI_TEST_TMPDIR}/expected-storage-failure"
		rm "${HUATUO_BAMAI_TEST_TMPDIR}/events"
		mkdir "${HUATUO_BAMAI_TEST_TMPDIR}/events"
		burst_input low
	fi
	burst_rounds=1
	[[ ${memburst_runtime} != churn ]] || burst_rounds=12
	for ((burst_round = 1; burst_round <= burst_rounds; burst_round++)); do
		burst_wait_samples low
		burst_input high
		wait_until 30 .1 burst_expected_count "${burst_round}" || fatal "${memburst_runtime}: capture ${burst_round} missing"
		assert_burst_runtime "${memburst_runtime}" "${burst_round}"
		if [[ ${memburst_runtime} == churn ]]; then
			if ((burst_round == 1)); then
				read -r burst_baseline_fds burst_baseline_rss burst_baseline_threads < <(burst_daemon_resources baseline) \
					|| fatal "cannot read daemon resource baseline"
			else
				assert_burst_resource_growth
			fi
		fi
		burst_input low
	done
	# A dead target must not poison later captures in the same daemon.
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" "${HUATUO_BAMAI_TEST_TMPDIR}/before-target-exit"
	kill -KILL "${burst_runtime_pid}"
	wait "${burst_runtime_pid}" 2> /dev/null || true
	burst_workloads=()
	mv "${HUATUO_BAMAI_TEST_TMPDIR}/commands" "${HUATUO_BAMAI_TEST_TMPDIR}/old-commands"
	burst_start_runtime go
	burst_wait_samples low
	burst_input high
	wait_until 30 .1 burst_expected_count "$((burst_rounds + 1))" || fatal "capture did not recover after ${memburst_runtime} exit"
	assert_burst_runtime go "$((burst_rounds + 1))"
	assert_memsnapshot_event_prefix "${HUATUO_BAMAI_TEST_TMPDIR}/before-target-exit" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" 'target exit changed completed events'
	burst_stop TERM
	kill -KILL "${burst_runtime_pid}"
	wait "${burst_runtime_pid}" 2> /dev/null || true
	burst_workloads=()
	log_info "PASS: standard daemon ${memburst_runtime} capture and target-exit recovery"
done
