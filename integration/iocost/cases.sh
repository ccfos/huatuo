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

# IOCOST qualification cases share fixtures and retain separate selectable modes.

set -euo pipefail

[[ ${BASH_SOURCE[0]} != "$0" ]] || {
	printf '[IOCOST OETEST][FAIL] invoke this case through integration/iocost/run.sh\n' >&2
	exit 1
}

# Functional waiting and accounting.

iocost_functional_direct_io() {
	local before after
	iocost_set_cost_profile high
	before=$(iocost_io_stat_value "${IOCOST_CGROUP_LEAF}" wios)
	iocost_run_leaf_write 1
	after=$(iocost_io_stat_value "${IOCOST_CGROUP_LEAF}" wios)
	((after > before)) \
		|| iocost_die "private leaf io.stat did not account direct I/O to ${IOCOST_DEVICE_ID}"
	iocost_log "direct I/O was charged to the private leaf"
}

iocost_functional_preexisting_waiter() {
	local workload_pid
	# Spend accumulated credit before creating one large bio whose wait begins
	# before the production object is loaded.  The bio must still be blocked at
	# the ready boundary, proving that its missing start hook cannot later be
	# mistaken for a complete episode by the wake hooks.
	iocost_set_cost_profile high
	sleep 1
	iocost_set_cost_profile diagnostic-long
	iocost_run_leaf_write 1 1048576
	iocost_set_cost_profile long
	# The loop splits this into sixteen 4 KiB bios.  At the long profile each
	# bio costs about two seconds (bandwidth plus IOPS), leaving ample time to
	# cross the ready boundary without exceeding the 120 second hard timeout.
	iocost_prepare_registered_write default "${IOCOST_CGROUP_LEAF}" 1 65536
	workload_pid=${IOCOST_LAST_PID}
	iocost_release_prepared_write "${workload_pid}"
	iocost_wait_for_blocked_write "${workload_pid}"

	iocost_start_qualification functional-preexisting-waiter 120
	iocost_wait_for_state "${workload_pid}" D 1 \
		|| iocost_die "preexisting waiter completed before the production ready boundary"
	iocost_wait_pid "${workload_pid}" \
		|| iocost_die "preexisting waiter workload failed"
	iocost_finish_qualification
	iocost_assert_empty_qualification_result \
		"${IOCOST_LAST_QUALIFICATION_RESULT}" "${IOCOST_DEVICE_ID}" \
		|| iocost_die "preexisting waiter polluted the production session"
	iocost_log "waiter admitted before attach produced no partial IOCOST episode"
}

iocost_functional_untracked_operation() {
	local before after
	# Both supported target kernels assign zero IOCOST cost to discard before
	# they can enqueue it.  A successful private-loop BLKDISCARD plus the cgroup
	# dios delta proves that the real non-READ/WRITE operation reached the block
	# layer; the production session must remain empty and healthy.
	iocost_set_cost_profile long
	iocost_start_qualification functional-untracked-operation 120
	before=$(iocost_io_stat_value "${IOCOST_CGROUP_LEAF}" dios)
	iocost_run_registered_discard default "${IOCOST_CGROUP_LEAF}"
	after=$(iocost_io_stat_value "${IOCOST_CGROUP_LEAF}" dios)
	((after > before)) \
		|| iocost_die "private loop io.stat did not account the discard operation"
	iocost_finish_qualification
	iocost_assert_empty_qualification_result \
		"${IOCOST_LAST_QUALIFICATION_RESULT}" "${IOCOST_DEVICE_ID}" \
		|| iocost_die "untracked discard operation polluted the production session"
	iocost_log "accounted discard operation produced no IOCOST wait series"
}

iocost_functional_repeated_diagnostic() {
	local pid result done log_file
	iocost_set_cost_profile high
	sleep 1
	iocost_set_cost_profile diagnostic-long
	iocost_start_diagnostic repeated 0
	pid=${IOCOST_LAST_PID}
	result=${IOCOST_LAST_RESULT}
	done=${IOCOST_RUNTIME_DONE_FILE}
	log_file=${IOCOST_RUNTIME_LOG_FILE}
	iocost_run_leaf_write 1 1048576
	iocost_finish_runtime "${pid}" "${result}" "${done}" "${log_file}"
	iocost_assert_diagnostic "${result}" repeated \
		|| iocost_die "diagnostic counters did not prove repeated -1 then final 0"
	iocost_log "diagnostic object proved repeated -1 returns and one final settlement"
}

iocost_condition_public_window() {
	local name=$1
	iocost_run_leaf_write 1
	iocost_scrape_once "condition-${name}"
	iocost_assert_metric_window "${IOCOST_LAST_SCRAPE}" any > /dev/null \
		|| iocost_die "conditioning window ${name} produced malformed metrics"
}

iocost_public_single_wait() {
	local name=$1
	local pid start_ns end_ns lower upper average
	iocost_start_leaf_write 1
	pid=${IOCOST_LAST_PID}
	start_ns=${IOCOST_LAST_RELEASE_NS}
	iocost_wait_for_blocked_write "${pid}"
	iocost_wait_pid "${pid}" || iocost_die "${name} waiter failed"
	end_ns=$(iocost_monotonic_ns)
	read -r lower upper < <(iocost_elapsed_bounds "${start_ns}" "${end_ns}") \
		|| iocost_die "cannot calculate ${name} elapsed-time tolerance"
	iocost_scrape_once "${name}"
	average=$(iocost_assert_metric_window "${IOCOST_LAST_SCRAPE}" exact 1 "${lower}" "${upper}") \
		|| iocost_die "${name} public count/average assertion failed"
	IOCOST_LAST_AVERAGE=${average}
}

iocost_functional_public_metrics() {
	local short_average long_average pid start_ns end_ns lower upper average
	iocost_set_cost_profile high
	sleep 1
	iocost_start_daemon

	iocost_run_leaf_write 1
	iocost_scrape_once under-budget
	iocost_assert_metric_window "${IOCOST_LAST_SCRAPE}" absent-or-zero > /dev/null \
		|| iocost_die "under-budget direct I/O was reported as a wait"

	iocost_set_cost_profile medium
	iocost_condition_public_window short
	iocost_public_single_wait short-wait
	short_average=${IOCOST_LAST_AVERAGE}

	iocost_set_cost_profile long
	iocost_condition_public_window long
	iocost_public_single_wait long-wait
	long_average=${IOCOST_LAST_AVERAGE}
	iocost_assert_float_greater "${long_average}" "${short_average}" \
		"different IOCOST waits must produce ordered public averages" \
		|| iocost_die "different-wait public average assertion failed"

	iocost_set_cost_profile cross
	iocost_condition_public_window cross
	# Start the next write while the cross-scrape profile keeps the IOCG over
	# budget. The first scrape happens while the only waiter is still pending;
	# completion must appear exactly once in the following successful scrape.
	iocost_start_leaf_write 1
	pid=${IOCOST_LAST_PID}
	start_ns=${IOCOST_LAST_RELEASE_NS}
	iocost_wait_for_blocked_write "${pid}"
	iocost_scrape_once cross-scrape-pending
	iocost_assert_metric_window "${IOCOST_LAST_SCRAPE}" exact 0 0 0 > /dev/null \
		|| iocost_die "cross-scrape waiter was published before completion"
	iocost_wait_pid "${pid}" || iocost_die "cross-scrape waiter failed"
	end_ns=$(iocost_monotonic_ns)
	read -r lower upper < <(iocost_elapsed_bounds "${start_ns}" "${end_ns}") \
		|| iocost_die "cannot calculate cross-scrape tolerance"
	iocost_scrape_once cross-scrape-complete
	average=$(iocost_assert_metric_window "${IOCOST_LAST_SCRAPE}" exact 1 "${lower}" "${upper}") \
		|| iocost_die "cross-scrape completion was not published exactly once"
	[[ -n ${average} ]] || iocost_die "cross-scrape average is empty"
	iocost_scrape_once cross-scrape-drained
	iocost_assert_metric_window "${IOCOST_LAST_SCRAPE}" exact 0 0 0 > /dev/null \
		|| iocost_die "cross-scrape completion was published more than once"

	iocost_stop_daemon
	iocost_log "public metrics proved under-budget, single, different and cross-scrape windows"
}

iocost_functional_510_offline() {
	local pid result done log_file
	grep -Eq '[[:space:]]ioc_pd_offline([[:space:]]|$)' /proc/kallsyms || {
		iocost_log "offline wake callback is unavailable"
		return 0
	}

	iocost_set_cost_rates 1024 1 1024 1
	iocost_run_leaf_write 1
	iocost_start_diagnostic offline 0
	pid=${IOCOST_LAST_PID}
	result=${IOCOST_LAST_RESULT}
	done=${IOCOST_RUNTIME_DONE_FILE}
	log_file=${IOCOST_RUNTIME_LOG_FILE}
	iocost_start_leaf_write 1
	local workload_pid=${IOCOST_LAST_PID}
	iocost_wait_for_blocked_write "${workload_pid}"
	printf '%s\n' "${workload_pid}" > "${IOCOST_CGROUP_PARKING}/cgroup.procs"
	[[ $(iocost_file_value "/proc/${workload_pid}/cgroup") == *"${IOCOST_CGROUP_PARKING#${IOCOST_CGROUP_ROOT}}"* ]] \
		|| iocost_die "offline workload was not moved to private parking cgroup"
	rmdir "${IOCOST_CGROUP_LEAF}"
	iocost_wait_pid "${workload_pid}" || iocost_die "offline waiter failed"
	iocost_finish_runtime "${pid}" "${result}" "${done}" "${log_file}"
	iocost_assert_diagnostic "${result}" offline \
		|| iocost_die "offline waiter did not settle through ret == 0"
	iocost_recreate_leaf
	iocost_log "cgroup offline waiter settled normally"
}

iocost_run_functional() {
	iocost_log "starting functional qualification"
	iocost_functional_direct_io
	iocost_functional_preexisting_waiter
	iocost_functional_untracked_operation

	# Observe uses the production object and is the only source of kernel
	# identity for diagnostic filtering. Shell never decodes a BPF map ABI.
	iocost_set_cost_profile long
	iocost_observe_identity

	# Observe, each diagnostic object and the production daemon are never live
	# concurrently. This keeps the test's own tracing programs from competing.
	iocost_functional_repeated_diagnostic
	iocost_functional_public_metrics
	iocost_functional_510_offline
	iocost_log "functional qualification completed"
}

# Cgroup and disk lifecycle.

iocost_lifecycle_assert_daemon_live() {
	iocost_pid_is_running_original "${IOCOST_DAEMON_PID}" \
		|| iocost_die "huatuo-bamai restarted or exited during lifecycle qualification"
}

iocost_lifecycle_assert_device_window() {
	local name=$1 file=$2 mode=$3
	local expected_count=${4:-0} min_ms=${5:-0} max_ms=${6:-0}
	local device_id=${IOCOST_REGISTERED_LOOP_DEVICE_ID[${name}]:-}
	[[ -n ${device_id} ]] || iocost_die "registered loop ${name} has no live device identity"
	(
		IOCOST_DEVICE_ID=${device_id}
		iocost_assert_metric_window "${file}" "${mode}" \
			"${expected_count}" "${min_ms}" "${max_ms}"
	)
}

iocost_lifecycle_run_wait() {
	local name=$1 cgroup=$2 description=$3 pid
	iocost_start_registered_write "${name}" "${cgroup}" 1
	pid=${IOCOST_LAST_PID}
	iocost_wait_for_blocked_write "${pid}"
	iocost_wait_pid "${pid}" || iocost_die "${description} waiter failed"
}

iocost_lifecycle_run_scsi_wait() {
	local description=$1 pid
	iocost_start_scsi_debug_write "${IOCOST_CGROUP_LEAF}"
	pid=${IOCOST_LAST_PID}
	iocost_wait_for_blocked_write "${pid}"
	iocost_wait_pid "${pid}" || iocost_die "${description} waiter failed"
}

iocost_lifecycle_assert_scsi_window() {
	local file=$1 mode=$2 expected_count=${3:-0} min_ms=${4:-0} max_ms=${5:-0}
	(
		IOCOST_DEVICE_ID=${IOCOST_SCSI_DEVICE_ID}
		iocost_assert_metric_window "${file}" "${mode}" \
			"${expected_count}" "${min_ms}" "${max_ms}"
	)
}

iocost_lifecycle_create_survivor_loop() {
	local backing=${IOCOST_TMP_DIR}/loop-survivor.backing
	iocost_create_loop_backing "${backing}"
	iocost_add_registered_loop_minor survivor "${backing}"
	iocost_attach_registered_loop survivor
}

iocost_lifecycle_cgroup_recreate() {
	local max_wait_ms=$((IOCOST_WAIT_TIMEOUT_SECONDS * 1000))
	# A previous mode in run.sh all may have removed another private leaf.
	# Drain that asynchronous release first so only this victim can change the
	# counter below; accepting an arbitrary nonzero baseline would allow two
	# unrelated releases to cross and falsely prove this generation was freed.
	iocost_wait_nr_dying_descendants "${IOCOST_CGROUP_PARENT}" 0 \
		|| iocost_die "private cgroup still has a prior dying descendant"

	# Leave one completed delta in the soon-to-be-deleted leaf and one in a
	# live sibling. Deleting the leaf may drop only its own unreported tail.
	iocost_lifecycle_run_wait default "${IOCOST_CGROUP_LEAF}" \
		"cgroup deletion victim"
	iocost_lifecycle_run_wait default "${IOCOST_CGROUP_PARKING}" \
		"cgroup deletion survivor"
	iocost_cgroup_is_empty "${IOCOST_CGROUP_LEAF}" \
		|| iocost_die "cgroup deletion victim still contains a process"
	rmdir "${IOCOST_CGROUP_LEAF}"
	iocost_wait_nr_dying_descendants "${IOCOST_CGROUP_PARENT}" 0 \
		|| iocost_die "deleted leaf did not reach the synchronous release boundary"

	# The recreated leaf's first eligible IO must be published in this next
	# scrape. Kernel pointer reuse is useful extra evidence, never a pass condition.
	iocost_recreate_leaf
	iocost_lifecycle_run_wait default "${IOCOST_CGROUP_LEAF}" \
		"recreated cgroup first wait"
	iocost_lifecycle_assert_daemon_live
	iocost_scrape_once lifecycle-cgroup-merged
	iocost_lifecycle_assert_device_window default "${IOCOST_LAST_SCRAPE}" exact 2 \
		0 "${max_wait_ms}" > /dev/null \
		|| iocost_die "cgroup deletion leaked its tail, lost its survivor, or delayed recreated data"
	iocost_scrape_once lifecycle-cgroup-drained
	iocost_lifecycle_assert_device_window default "${IOCOST_LAST_SCRAPE}" exact 0 \
		0 0 > /dev/null || iocost_die "cgroup lifecycle window was published more than once"
	iocost_log "cgroup deletion dropped only its tail and recreated data was immediate"
}

iocost_lifecycle_disk_recreate() {
	local target_minor target_device_id recreated_backing
	local max_wait_ms=$((IOCOST_WAIT_TIMEOUT_SECONDS * 1000))
	target_minor=${IOCOST_REGISTERED_LOOP_MINOR[default]}
	target_device_id=${IOCOST_REGISTERED_LOOP_DEVICE_ID[default]}
	recreated_backing=${IOCOST_TMP_DIR}/loop-default-recreated.backing
	iocost_create_loop_backing "${recreated_backing}"

	# Keep a completed delta on another live IOC while the target IOC carries
	# an unreported tail. A global BPF session restart would lose the survivor.
	iocost_lifecycle_run_wait default "${IOCOST_CGROUP_LEAF}" \
		"disk deletion victim"
	iocost_lifecycle_run_wait survivor "${IOCOST_CGROUP_PARKING}" \
		"disk deletion survivor"
	iocost_remove_registered_loop default
	iocost_lifecycle_assert_daemon_live

	# LOOP_CTL_ADD must recreate the exact owned minor; add/attach revalidates
	# devtmpfs, sysfs, backing-file echo, direct I/O and topology before use.
	iocost_add_registered_loop_minor default "${recreated_backing}" "${target_minor}"
	iocost_attach_registered_loop default
	[[ ${IOCOST_REGISTERED_LOOP_DEVICE_ID[default]} == "${target_device_id}" ]] \
		|| iocost_die "recreated target did not retain its exact major:first_minor"
	iocost_set_registered_cost_profile default long
	iocost_lifecycle_run_wait default "${IOCOST_CGROUP_LEAF}" \
		"recreated disk first wait"
	iocost_lifecycle_assert_daemon_live

	iocost_scrape_once lifecycle-disk-merged
	iocost_lifecycle_assert_device_window default "${IOCOST_LAST_SCRAPE}" exact 1 \
		0 "${max_wait_ms}" > /dev/null \
		|| iocost_die "disk deletion leaked its tail or delayed recreated data"
	iocost_lifecycle_assert_device_window survivor "${IOCOST_LAST_SCRAPE}" exact 1 \
		0 "${max_wait_ms}" > /dev/null \
		|| iocost_die "disk deletion restarted the healthy session or lost the survivor"
	iocost_scrape_once lifecycle-disk-drained
	iocost_lifecycle_assert_device_window default "${IOCOST_LAST_SCRAPE}" exact 0 \
		0 0 > /dev/null || iocost_die "recreated disk data was published more than once"
	iocost_lifecycle_assert_device_window survivor "${IOCOST_LAST_SCRAPE}" exact 0 \
		0 0 > /dev/null || iocost_die "disk survivor data was published more than once"
	iocost_log "disk removal dropped only its tail and exact-minor recreation was immediate"
}

iocost_lifecycle_scsi_expected_count() {
	python3 - "$@" << 'PY'
import json
import sys

before_path, after_path, old_device, new_device = sys.argv[1:]

def identity(path, device):
    with open(path, encoding="utf-8") as source:
        result = json.load(source)
    if result.get("error") or result.get("drain") != {
        "pending_rows": 0, "active_wake_frames": 0, "drained": True,
    }:
        raise SystemExit("SCSI identity observation did not drain successfully")
    iocs = result.get("observation", {}).get("iocs", {})
    matches = [ioc_id for ioc_id, dev in iocs.items() if dev == device]
    if len(matches) != 1 or int(matches[0], 16) == 0:
        raise SystemExit(f"expected one live IOC for {device}, got {matches}")
    return matches[0]

old_id = identity(before_path, old_device)
new_id = identity(after_path, new_device)
print(f"SCSI rebind IOC: {old_id} -> {new_id}", file=sys.stderr)
print(3 if old_id == new_id else 2)
PY
}

iocost_lifecycle_scsi_rebind() {
	local old_device_id scsi_peer expected_count
	local max_wait_ms=$((IOCOST_WAIT_TIMEOUT_SECONDS * 1000))
	iocost_create_scsi_debug_device
	iocost_create_named_leaf scsi-peer
	scsi_peer=${IOCOST_LAST_CGROUP}
	iocost_set_scsi_debug_cost_profile long
	old_device_id=${IOCOST_SCSI_DEVICE_ID}
	iocost_start_qualification scsi-rebind 120 TEST_IOCOST_CHECKPOINT=1

	# Keep all SCSI completions unpublished. The same observer session tells
	# whether this kernel retains the IOC or destroys it during sd unbind.
	iocost_lifecycle_run_scsi_wait "pre-rebind IOC wait"
	iocost_lifecycle_run_wait survivor "${IOCOST_CGROUP_PARKING}" \
		"retained queue survivor"
	iocost_checkpoint_qualification
	# A fast cost model lets the automatic udev probe finish after sd bind.
	# Changing the model does not disable IOCOST or reset its accumulated data.
	iocost_set_scsi_debug_cost_profile high
	iocost_unbind_scsi_debug_disk
	iocost_rebind_scsi_debug_disk
	iocost_set_scsi_debug_cost_profile long
	# Both IOCGs must account to the IOC currently owning this device.
	iocost_begin_write_batch
	iocost_prepare_scsi_debug_batch_write "${IOCOST_CGROUP_LEAF}"
	iocost_prepare_scsi_debug_batch_write "${scsi_peer}"
	iocost_release_write_batch
	iocost_wait_write_batch_blocked
	iocost_wait_write_batch
	iocost_lifecycle_assert_daemon_live
	iocost_finish_qualification
	iocost_assert_runtime_common "${IOCOST_LAST_QUALIFICATION_RESULT}" 0 \
		|| iocost_die "SCSI lifecycle observer failed"
	expected_count=$(iocost_lifecycle_scsi_expected_count \
		"${IOCOST_LAST_QUALIFICATION_RESULT}.checkpoint" \
		"${IOCOST_LAST_QUALIFICATION_RESULT}" \
		"${old_device_id}" "${IOCOST_SCSI_DEVICE_ID}") \
		|| iocost_die "cannot determine the actual SCSI IOC lifecycle"

	iocost_scrape_once lifecycle-scsi-rebind-merged
	iocost_lifecycle_assert_scsi_window "${IOCOST_LAST_SCRAPE}" exact "${expected_count}" \
		0 "${max_wait_ms}" > /dev/null \
		|| iocost_die "SCSI rebind completion count disagrees with the actual IOC lifetime"
	iocost_lifecycle_assert_device_window survivor "${IOCOST_LAST_SCRAPE}" exact 1 \
		0 "${max_wait_ms}" > /dev/null \
		|| iocost_die "retained request_queue handling restarted the healthy session"
	if [[ ${IOCOST_SCSI_PREVIOUS_DEVICE_ID} != "${IOCOST_SCSI_DEVICE_ID}" ]]; then
		(
			IOCOST_DEVICE_ID=${old_device_id}
			iocost_assert_metric_window "${IOCOST_LAST_SCRAPE}" absent-or-zero 0 0 0
		) > /dev/null \
			|| iocost_die "retired scsi_debug device identity published stale data"
	fi

	iocost_scrape_once lifecycle-scsi-rebind-drained
	iocost_lifecycle_assert_scsi_window "${IOCOST_LAST_SCRAPE}" exact 0 0 0 \
		> /dev/null || iocost_die "rebound scsi_debug data was published more than once"
	iocost_lifecycle_assert_device_window survivor "${IOCOST_LAST_SCRAPE}" exact 0 \
		0 0 > /dev/null || iocost_die "retained-queue survivor was published more than once"
	iocost_remove_named_leaf scsi-peer
	iocost_cleanup_scsi_debug || iocost_die "cannot remove the owned scsi_debug fixture"
	iocost_lifecycle_assert_daemon_live
	iocost_log "sd unbind/rebind followed the actual IOC lifetime"
}

iocost_run_lifecycle() {
	iocost_log "starting lifecycle qualification"
	iocost_lifecycle_create_survivor_loop
	iocost_set_registered_cost_profile default long
	iocost_set_registered_cost_profile survivor long
	iocost_start_daemon
	iocost_lifecycle_cgroup_recreate
	iocost_lifecycle_disk_recreate
	iocost_lifecycle_scsi_rebind
	iocost_stop_daemon
	iocost_log "lifecycle qualification completed"
}

# Map-helper failure handling.

iocost_condition_fault_admission() {
	# Spend any accumulated budget outside the diagnostic object, then make a
	# single 4 KiB request expensive enough to remain observable while the
	# freshly loaded object is active.
	iocost_set_cost_profile high
	sleep 1
	iocost_set_cost_profile diagnostic-long
	iocost_run_leaf_write 1 1048576
	# IOCOST rounds the byte-rate coefficient to pages per second.  One
	# 4 KiB page/s (and one IO/s) gives this request a deterministic roughly
	# one-second cost: comfortably visible to the 10 ms D-state polling loop,
	# while remaining well inside the workload and runtime deadlines.
	iocost_set_cost_rates 4096 1 4096 1
}

iocost_run_fault_case() {
	local name=$1 fault_mask=$2
	local pid result done log_file workload_pid

	iocost_condition_fault_admission
	iocost_start_diagnostic "fault-${name}" "${fault_mask}"
	pid=${IOCOST_LAST_PID}
	result=${IOCOST_LAST_RESULT}
	done=${IOCOST_RUNTIME_DONE_FILE}
	log_file=${IOCOST_RUNTIME_LOG_FILE}

	# One direct-I/O block is the sole target admission while this diagnostic
	# object is loaded. The runtime counters prove the guarded admission count.
	iocost_start_leaf_write 1
	workload_pid=${IOCOST_LAST_PID}
	iocost_wait_for_blocked_write "${workload_pid}"
	iocost_wait_pid "${workload_pid}" \
		|| iocost_die "${name} fault workload failed"

	iocost_finish_runtime "${pid}" "${result}" "${done}" "${log_file}"
	iocost_assert_fault_diagnostic "${result}" "${name}" \
		|| iocost_die "${name} diagnostic fault evidence is invalid"
	iocost_log "diagnostic object verified ${name} status, settlement, and drain"
}

iocost_run_faults() {
	iocost_log "starting deterministic fault qualification"

	# Observe uses the production object and supplies the exact live owner
	# identity used by every subsequently loaded diagnostic object.
	iocost_set_cost_profile long
	iocost_observe_identity

	iocost_run_fault_case map-full 1
	iocost_run_fault_case collision 2
	iocost_run_fault_case delete-failure 4

	iocost_log "deterministic fault qualification completed"
}

# Concurrent workload and capacity pressure.

readonly IOCOST_PRESSURE_NO_WAIT_ISSUERS=32
readonly IOCOST_PRESSURE_SHORT_WAIT_ISSUERS=64
readonly IOCOST_PRESSURE_DISK_CHURN_GENERATIONS=3
readonly IOCOST_PRESSURE_SECONDARY_LOOP=pressure-secondary

IOCOST_PRESSURE_LAST_WAIT_LOWER=""
IOCOST_PRESSURE_LAST_WAIT_UPPER=""

iocost_pressure_run_observed_wait() {
	local loop_name=$1 cgroup=$2 description=$3 pid start_ns end_ns
	iocost_prepare_registered_write "${loop_name}" "${cgroup}" 1
	pid=${IOCOST_LAST_PID}
	iocost_release_prepared_write "${pid}"
	start_ns=${IOCOST_LAST_RELEASE_NS}
	iocost_wait_for_blocked_write "${pid}"
	iocost_wait_pid "${pid}" || iocost_die "${description} failed"
	end_ns=$(iocost_monotonic_ns)
	read -r IOCOST_PRESSURE_LAST_WAIT_LOWER IOCOST_PRESSURE_LAST_WAIT_UPPER < <(
		iocost_elapsed_bounds "${start_ns}" "${end_ns}"
	) || iocost_die "cannot calculate ${description} elapsed envelope"
}

iocost_pressure_average_bounds() {
	(($# > 0 && $# % 2 == 0)) \
		|| iocost_die "average envelope requires lower/upper pairs"
	python3 - "$@" << 'PY'
import math
import sys

values = list(map(float, sys.argv[1:]))
lowers = values[0::2]
uppers = values[1::2]
if any(not math.isfinite(value) or value < 0 for value in values):
    raise SystemExit("invalid non-finite or negative wait envelope")
if any(lower > upper for lower, upper in zip(lowers, uppers)):
    raise SystemExit("invalid reversed wait envelope")
print(f"{sum(lowers) / len(lowers):.9f} {sum(uppers) / len(uppers):.9f}")
PY
}

iocost_pressure_create_secondary_loop() {
	local backing
	if [[ ${IOCOST_REGISTERED_LOOP_ATTACHED[${IOCOST_PRESSURE_SECONDARY_LOOP}]:-0} == 1 ]]; then
		return 0
	fi
	backing=${IOCOST_TMP_DIR}/loop-${IOCOST_PRESSURE_SECONDARY_LOOP}.backing
	iocost_create_loop_backing "${backing}"
	iocost_add_registered_loop_minor "${IOCOST_PRESSURE_SECONDARY_LOOP}" "${backing}"
	iocost_attach_registered_loop "${IOCOST_PRESSURE_SECONDARY_LOOP}"
}

iocost_pressure_no_wait() {
	local issuer
	iocost_set_cost_profile high
	sleep 1
	iocost_start_qualification pressure-no-wait "${IOCOST_WAIT_TIMEOUT_SECONDS}"
	iocost_begin_write_batch
	for ((issuer = 1; issuer <= IOCOST_PRESSURE_NO_WAIT_ISSUERS; issuer++)); do
		iocost_prepare_batch_write default "${IOCOST_CGROUP_LEAF}" 1
	done
	iocost_release_write_batch
	iocost_wait_write_batch
	iocost_finish_qualification
	iocost_assert_qualification_result "${IOCOST_LAST_QUALIFICATION_RESULT}" \
		"${IOCOST_DEVICE_ID},0,0,0"
	iocost_log "32 prepared 4 KiB issuers completed under the high profile without waits"
}

iocost_pressure_short_waits() {
	local issuer name lower upper
	local -a bounds=()
	for ((issuer = 1; issuer <= IOCOST_PRESSURE_SHORT_WAIT_ISSUERS; issuer++)); do
		name=pressure-short-${issuer}
		iocost_create_named_leaf "${name}"
	done
	iocost_set_cost_profile high
	sleep 1
	iocost_set_cost_profile medium
	iocost_start_qualification pressure-short-waits "${IOCOST_WAIT_TIMEOUT_SECONDS}"
	for ((issuer = 1; issuer <= IOCOST_PRESSURE_SHORT_WAIT_ISSUERS; issuer++)); do
		name=pressure-short-${issuer}
		iocost_pressure_run_observed_wait default \
			"${IOCOST_CGROUP_PARENT}/${name}" \
			"short-wait issuer ${issuer}"
		bounds+=(
			"${IOCOST_PRESSURE_LAST_WAIT_LOWER}"
			"${IOCOST_PRESSURE_LAST_WAIT_UPPER}"
		)
	done
	read -r lower upper < <(iocost_pressure_average_bounds "${bounds[@]}") \
		|| iocost_die "cannot calculate short-wait cohort elapsed envelope"
	iocost_finish_qualification
	iocost_assert_qualification_result "${IOCOST_LAST_QUALIFICATION_RESULT}" \
		"${IOCOST_DEVICE_ID},64,${lower},${upper}"
	iocost_log "64 fresh cgroup issuers produced 64 individually observed short waits"
}

iocost_pressure_severe_wait() {
	local name=pressure-severe path pid start_ns end_ns lower upper
	iocost_create_named_leaf "${name}"
	path=${IOCOST_CGROUP_PARENT}/${name}
	iocost_set_cost_profile high
	sleep 1
	iocost_set_cost_profile long
	iocost_start_qualification pressure-severe "${IOCOST_WAIT_TIMEOUT_SECONDS}"
	iocost_prepare_registered_write default "${path}" 1
	pid=${IOCOST_LAST_PID}
	iocost_release_prepared_write "${pid}"
	start_ns=${IOCOST_LAST_RELEASE_NS}
	iocost_wait_for_blocked_write "${pid}"
	iocost_wait_pid "${pid}" || iocost_die "severe wait issuer failed"
	end_ns=$(iocost_monotonic_ns)
	read -r lower upper < <(iocost_elapsed_bounds "${start_ns}" "${end_ns}") \
		|| iocost_die "cannot calculate severe-wait elapsed envelope"
	iocost_finish_qualification
	iocost_assert_qualification_result "${IOCOST_LAST_QUALIFICATION_RESULT}" \
		"${IOCOST_DEVICE_ID},1,${lower},${upper}"
	iocost_log "one severe wait matched its measured elapsed-time envelope"
}

iocost_pressure_multi_device() {
	local default_one default_two secondary_one secondary_two secondary_device
	local default_lower default_upper secondary_lower secondary_upper
	local name
	local -a default_bounds=() secondary_bounds=()
	iocost_pressure_create_secondary_loop
	default_one=pressure-multi-default-one
	default_two=pressure-multi-default-two
	secondary_one=pressure-multi-secondary-one
	secondary_two=pressure-multi-secondary-two
	iocost_create_named_leaf "${default_one}"
	iocost_create_named_leaf "${default_two}"
	iocost_create_named_leaf "${secondary_one}"
	iocost_create_named_leaf "${secondary_two}"
	secondary_device=${IOCOST_REGISTERED_LOOP_DEVICE_ID[${IOCOST_PRESSURE_SECONDARY_LOOP}]}

	iocost_set_registered_cost_profile default high
	iocost_set_registered_cost_profile "${IOCOST_PRESSURE_SECONDARY_LOOP}" high
	sleep 1
	iocost_set_registered_cost_profile default medium
	iocost_set_registered_cost_profile "${IOCOST_PRESSURE_SECONDARY_LOOP}" medium
	iocost_start_qualification pressure-multi-device "${IOCOST_WAIT_TIMEOUT_SECONDS}"
	for name in "${default_one}" "${default_two}"; do
		iocost_pressure_run_observed_wait default \
			"${IOCOST_CGROUP_PARENT}/${name}" "default-device ${name}"
		default_bounds+=(
			"${IOCOST_PRESSURE_LAST_WAIT_LOWER}"
			"${IOCOST_PRESSURE_LAST_WAIT_UPPER}"
		)
	done
	for name in "${secondary_one}" "${secondary_two}"; do
		iocost_pressure_run_observed_wait "${IOCOST_PRESSURE_SECONDARY_LOOP}" \
			"${IOCOST_CGROUP_PARENT}/${name}" "secondary-device ${name}"
		secondary_bounds+=(
			"${IOCOST_PRESSURE_LAST_WAIT_LOWER}"
			"${IOCOST_PRESSURE_LAST_WAIT_UPPER}"
		)
	done
	read -r default_lower default_upper < <(
		iocost_pressure_average_bounds "${default_bounds[@]}"
	) || iocost_die "cannot calculate default-device elapsed envelope"
	read -r secondary_lower secondary_upper < <(
		iocost_pressure_average_bounds "${secondary_bounds[@]}"
	) || iocost_die "cannot calculate secondary-device elapsed envelope"
	iocost_finish_qualification
	iocost_assert_qualification_result "${IOCOST_LAST_QUALIFICATION_RESULT}" \
		"${IOCOST_DEVICE_ID},2,${default_lower},${default_upper}" \
		"${secondary_device},2,${secondary_lower},${secondary_upper}"
	iocost_log "two devices and two fresh leaves per device published exact counts"
}

iocost_pressure_disk_churn() {
	local survivor_leaf=pressure-disk-survivor target_leaf=pressure-disk-target
	local target_minor target_device_id target_backing survivor_device generation
	local survivor_lower survivor_upper target_lower target_upper
	iocost_pressure_create_secondary_loop
	iocost_create_named_leaf "${survivor_leaf}"
	iocost_create_named_leaf "${target_leaf}"
	target_minor=${IOCOST_REGISTERED_LOOP_MINOR[default]}
	target_device_id=${IOCOST_REGISTERED_LOOP_DEVICE_ID[default]}
	target_backing=${IOCOST_REGISTERED_LOOP_BACKING[default]}
	survivor_device=${IOCOST_REGISTERED_LOOP_DEVICE_ID[${IOCOST_PRESSURE_SECONDARY_LOOP}]}

	iocost_set_registered_cost_profile default high
	iocost_set_registered_cost_profile "${IOCOST_PRESSURE_SECONDARY_LOOP}" high
	sleep 1
	iocost_set_registered_cost_profile default medium
	iocost_set_registered_cost_profile "${IOCOST_PRESSURE_SECONDARY_LOOP}" medium
	iocost_start_qualification pressure-disk-churn \
		"${IOCOST_WAIT_TIMEOUT_SECONDS}"

	# Keep one completion on another live IOC. An implementation that handles
	# target deletion by restarting the production object must lose this row.
	iocost_pressure_run_observed_wait "${IOCOST_PRESSURE_SECONDARY_LOOP}" \
		"${IOCOST_CGROUP_PARENT}/${survivor_leaf}" "disk churn survivor"
	survivor_lower=${IOCOST_PRESSURE_LAST_WAIT_LOWER}
	survivor_upper=${IOCOST_PRESSURE_LAST_WAIT_UPPER}
	for ((generation = 1; generation <= IOCOST_PRESSURE_DISK_CHURN_GENERATIONS; generation++)); do
		if ((generation == 1)); then
			# This deleted generation is the high-contrast stale-wait marker.
			iocost_set_registered_cost_profile default long
		fi
		iocost_pressure_run_observed_wait default \
			"${IOCOST_CGROUP_PARENT}/${target_leaf}" \
			"disk churn generation ${generation}"
		if ((generation == IOCOST_PRESSURE_DISK_CHURN_GENERATIONS)); then
			target_lower=${IOCOST_PRESSURE_LAST_WAIT_LOWER}
			target_upper=${IOCOST_PRESSURE_LAST_WAIT_UPPER}
		fi
		if ((generation < IOCOST_PRESSURE_DISK_CHURN_GENERATIONS)); then
			iocost_remove_registered_loop default
			iocost_add_registered_loop_minor default "${target_backing}" "${target_minor}"
			iocost_attach_registered_loop default
			[[ ${IOCOST_REGISTERED_LOOP_DEVICE_ID[default]} == "${target_device_id}" ]] \
				|| iocost_die "disk churn did not recreate exact device ${target_device_id}"
			iocost_set_registered_cost_profile default medium
		fi
	done

	iocost_finish_qualification
	iocost_assert_qualification_result "${IOCOST_LAST_QUALIFICATION_RESULT}" \
		"${target_device_id},1,${target_lower},${target_upper}" \
		"${survivor_device},1,${survivor_lower},${survivor_upper}"
	iocost_assert_runtime_common "${IOCOST_LAST_QUALIFICATION_RESULT}" 0 healthy 2 2 2
	iocost_log "${IOCOST_PRESSURE_DISK_CHURN_GENERATIONS} exact-minor disk generations retained only both live final rows"
}

iocost_run_pressure() {
	iocost_log "starting fixed IOCOST correctness pressure and churn qualification"
	iocost_pressure_no_wait
	iocost_pressure_short_waits
	iocost_pressure_severe_wait
	iocost_pressure_multi_device
	iocost_pressure_disk_churn
	iocost_log "fixed IOCOST correctness pressure and churn qualification completed"
}

# Kernel compatibility and live ABI.

iocost_compat_assert_exact_test_log() {
	local log_file=$1 name=$2
	python3 - "${log_file}" "${name}" << 'PY'
import re
import sys

path, name = sys.argv[1:]
if not re.fullmatch(r"Test[A-Za-z0-9_]+", name):
    raise SystemExit(f"invalid exact Go test name: {name!r}")
with open(path, encoding="utf-8") as source:
    lines = source.read().splitlines()

expected_run = f"=== RUN   {name}"
if lines.count(expected_run) != 1:
    raise SystemExit(f"expected one exact RUN line for {name}: {lines!r}")
for line in lines:
    if not line.startswith("=== RUN   "):
        continue
    selected = line[len("=== RUN   "):]
    if selected != name and not selected.startswith(name + "/"):
        raise SystemExit(f"unselected Go test ran: {line!r}")

top_level_passes = [
    line for line in lines
    if line.startswith("--- PASS: ")
]
if len(top_level_passes) != 1 or not re.fullmatch(
    rf"--- PASS: {re.escape(name)} \([^)]+\)", top_level_passes[0]
):
    raise SystemExit(f"expected one exact PASS line for {name}: {top_level_passes!r}")
if any(line.lstrip().startswith("--- SKIP: ") for line in lines):
    raise SystemExit(f"Go test skipped required coverage: {name}")
if any(line.lstrip().startswith("--- FAIL: ") for line in lines):
    raise SystemExit(f"Go test reported a failed test or subtest: {name}")
if any("testing: warning: no tests to run" in line for line in lines):
    raise SystemExit(f"Go test selected no tests: {name}")
if lines.count("PASS") != 1:
    raise SystemExit(f"Go test lacks one final PASS line: {name}")
PY
}

iocost_compat_run_exact_test() {
	local name=$1
	local log_file=${IOCOST_TMP_DIR}/compat-${name}.log pid
	local record_failed=0
	[[ ${name} =~ ^Test[A-Za-z0-9_]+$ ]] \
		|| iocost_die "invalid exact Go test name: ${name}"

	iocost_signal_critical_enter
	# Even the smallest fixture test must become an owned process before it can
	# exit. The self-STOP barrier removes the spawn-to-registration race while
	# exec preserves the registered PID and process group for bounded cleanup.
	setsid bash -c '
		kill -STOP "$$"
		set -euo pipefail
		(($# >= 2)) || exit 1
		root_dir=$1
		[[ -n ${root_dir} ]] || exit 1
		shift
		cd -- "${root_dir}" || exit 1
		exec env \
			-u TEST_IOCOST_DIAG_FAULT_MASK \
			-u TEST_IOCOST_DIAG_MAJOR \
			-u TEST_IOCOST_DIAG_FIRST_MINOR \
			-u TEST_IOCOST_DIAG_CSS_SERIAL \
			-u TEST_IOCOST_DIAG_IOCG_PTR \
			-u TEST_IOCOST_OBSERVE_MAJOR \
			-u TEST_IOCOST_OBSERVE_FIRST_MINOR \
			-u TEST_IOCOST_READY_FILE \
			-u TEST_IOCOST_DONE_FILE \
			-u TEST_IOCOST_RESULT_FILE \
			TEST_IOCOST_REQUIRED=1 \
			TEST_IOCOST_OBJECT=production \
			TEST_IOCOST_TIMEOUT_SECONDS=120 \
			"$@"
	' iocost-compat-test \
		"${IOCOST_ROOT_DIR}" \
		"${IOCOST_ROOT_DIR}/${IOCOST_RUNTIME_TEST_REL}" \
		-test.run "^${name}$" -test.v > "${log_file}" 2>&1 &
	pid=$!
	iocost_record_pid "${pid}" "compat-${name}" || record_failed=1
	iocost_signal_critical_leave
	((record_failed == 0)) \
		|| iocost_die "cannot register exact Go test ${name} pid ${pid}"
	iocost_wait_for_state "${pid}" T 5 \
		|| iocost_die "exact Go test ${name} did not reach its ownership barrier"
	kill -CONT "${pid}" \
		|| iocost_die "cannot release exact Go test ${name} from its ownership barrier"
	if ! iocost_wait_pid "${pid}"; then
		[[ ! -f ${log_file} ]] || sed 's/^/[compat-test] /' "${log_file}" >&2
		iocost_die "required exact Go test failed: ${name}"
	fi
	if ! iocost_compat_assert_exact_test_log "${log_file}" "${name}"; then
		sed 's/^/[compat-test] /' "${log_file}" >&2
		iocost_die "required exact Go test output is invalid: ${name}"
	fi
}

iocost_compat_resolver_fixtures() {
	iocost_compat_run_exact_test TestLoadIOCostKernelProfileUsesOneSymbolSnapshot
	iocost_compat_run_exact_test TestResolveIOCostKallsymsRejectsMissingRequiredSymbols
	iocost_compat_run_exact_test TestResolveIOCostKallsymsRequiresEnqueueCaller
	iocost_compat_run_exact_test TestIOCostObjectDeviceFieldSelection
	iocost_log "precompiled hook and CO-RE fixtures passed exactly"
}

iocost_compat_live_abi() {
	# This is the production object with the live target kernel's BTF, symbols,
	# verifier and every attachment required by the live kernel profile.
	# Required mode converts every missing prerequisite into failure; the
	# exact-output check additionally forbids skip.
	iocost_compat_run_exact_test TestIOCostStartAttachSmoke
	iocost_log "production object loaded and attached to the live kernel"
}

iocost_compat_condition_single_wait() {
	# Remove accumulated credit before the production qualification object is
	# loaded, then make its sole 4 KiB business write a deterministic long wait.
	iocost_set_cost_profile high
	sleep 1
	iocost_set_cost_profile diagnostic-long
	iocost_run_leaf_write 1 1048576
	iocost_set_cost_profile long
}

iocost_compat_assert_single_repeated_waiter() {
	local result=$1
	python3 - "${result}" << 'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    counters = json.load(source).get("diagnostic_counters")
if counters["start_guard_passes"] != 1:
    raise SystemExit(f"diagnostic did not admit exactly one waiter: {counters!r}")
PY
}

iocost_compat_live_function() {
	local workload_pid pid result done log_file
	local start_ns end_ns lower upper

	# Production evidence comes first and captures two consecutive collector
	# intervals from the same session: one exact completion followed by zero.
	iocost_compat_condition_single_wait
	iocost_start_qualification compat-live-function 120
	iocost_prepare_registered_write default "${IOCOST_CGROUP_LEAF}" 1
	workload_pid=${IOCOST_LAST_PID}
	iocost_release_prepared_write "${workload_pid}"
	start_ns=${IOCOST_LAST_RELEASE_NS}
	iocost_wait_for_blocked_write "${workload_pid}"
	iocost_wait_pid "${workload_pid}" \
		|| iocost_die "production live-function waiter failed"
	end_ns=$(iocost_monotonic_ns)
	read -r lower upper < <(iocost_elapsed_bounds "${start_ns}" "${end_ns}") \
		|| iocost_die "cannot calculate production live-function elapsed envelope"
	iocost_finish_qualification
	iocost_assert_qualification_result \
		"${IOCOST_LAST_QUALIFICATION_RESULT}" \
		"${IOCOST_DEVICE_ID},1,${lower},${upper}" \
		|| iocost_die "production live-function count, average, drain, or status is invalid"

	# The production observe object supplies the exact current fixture identity.
	# It is fully stopped before the fresh diagnostic object is loaded.
	iocost_observe_identity

	# One large direct-I/O bio remains one guarded waiter long enough to exercise
	# repeated ret == -1 wakeups before its single final ret == 0 settlement.
	iocost_set_cost_profile high
	sleep 1
	iocost_set_cost_profile diagnostic-long
	iocost_start_diagnostic compat-repeated 0
	pid=${IOCOST_LAST_PID}
	result=${IOCOST_LAST_RESULT}
	done=${IOCOST_RUNTIME_DONE_FILE}
	log_file=${IOCOST_RUNTIME_LOG_FILE}
	iocost_start_leaf_write 1 1048576
	workload_pid=${IOCOST_LAST_PID}
	iocost_wait_for_blocked_write "${workload_pid}"
	iocost_wait_pid "${workload_pid}" \
		|| iocost_die "diagnostic repeated waiter failed"
	iocost_finish_runtime "${pid}" "${result}" "${done}" "${log_file}"
	iocost_assert_diagnostic "${result}" repeated \
		|| iocost_die "diagnostic repeated-wake evidence failed common validation"
	iocost_compat_assert_single_repeated_waiter "${result}" \
		|| iocost_die "diagnostic live-function cardinality is invalid"
	iocost_log "live function preserved production accounting and diagnostic hook semantics"
}

iocost_run_compat() {
	iocost_log "starting kernel compatibility qualification"
	iocost_compat_resolver_fixtures
	iocost_compat_live_abi
	iocost_compat_live_function
	iocost_log "kernel compatibility qualification completed"
}
