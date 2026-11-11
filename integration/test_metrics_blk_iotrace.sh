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

# Check host BlkIOTrace histograms and externally controlled response delays.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
io_test_isolate_run_directory

run_host_latency() {
	set -euo pipefail
	local scenario=${1:-host}

	source "${ROOT_DIR}/integration/lib.sh"
	source "${ROOT_DIR}/integration/config.sh"
	source "${ROOT_DIR}/integration/lib_io.sh"
	source "${ROOT_DIR}/integration/lib_blk_iotrace.sh"
	CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
	WAIT_HUATUO_BAMAI_TIMEOUT=30
	require_commands fio jq timeout flock

	readonly BLK_IOTRACE_WORK="${HUATUO_BAMAI_TEST_TMPDIR}/blk_iotrace"
	mkdir -p "${BLK_IOTRACE_WORK}"
	readonly BLK_IOTRACE_DELAY_DIR=/sys/bus/pseudo/drivers/scsi_debug
	blk_iotrace_delay_restore_pending=0
	blk_iotrace_original_delay=""
	blk_iotrace_original_ndelay=""
	blk_iotrace_disk_lock=""

	cleanup_blk_iotrace_metrics() {
		local status=$?
		trap - EXIT
		trap '' INT TERM
		set +e
		huatuo_bamai_stop || status=1
		[[ -z ${blk_iotrace_disk_lock} ]] || exec {blk_iotrace_disk_lock}<&-
		blk_iotrace_disk_lock=""
		if ((blk_iotrace_delay_restore_pending)) && ! restore_blk_iotrace_delay; then
			log_error "cannot restore owned scsi_debug delay parameters; inspect ${BLK_IOTRACE_WORK}/delay-resources.txt"
			status=1
		fi
		if ! io_test_cleanup_disks; then
			log_error "cannot reclaim BlkIOTrace disks: ${io_test_disks[*]}; verify scsi_debug owner ${io_test_scsi_owner} before removing it"
			status=1
		fi
		exit "${status}"
	}

	trap cleanup_blk_iotrace_metrics EXIT
	trap 'log_error "BlkIOTrace test interrupted by INT; entering cleanup"; exit 130' INT
	trap 'log_error "BlkIOTrace test interrupted by TERM or execution timeout; entering cleanup"; exit 143' TERM
	if [[ ${scenario} == partition ]]; then
		io_test_prepare_scsi_disks 1 1
		((io_test_scsi_owned)) || skip "partition remap requires an available scsi_debug module"
	else
		io_test_prepare_disks
	fi
	trap 'log_error "BlkIOTrace test interrupted by INT; entering cleanup"; exit 130' INT
	trap 'log_error "BlkIOTrace test interrupted by TERM or execution timeout; entering cleanup"; exit 143' TERM
	blk_iotrace_device="/dev/${io_test_disks[0]}"
	blk_iotrace_disk=${blk_iotrace_device##*/}
	printf 'device=%s\nowned=%s\nwritable=%s\nscsi_debug_owner=%s\n' \
		"${blk_iotrace_device}" "${io_test_scsi_owned}" "${io_test_disk_writable}" "${io_test_scsi_owner}" > "${BLK_IOTRACE_WORK}/resources.txt"

	write_config_bamai_blk_iotrace_metrics() {
		write_config_bamai_sched_tick | sed 's/"blk_iotrace"/"sched_tick"/'
	}

	scrape_blk_iotrace() {
		local label=$1
		blk_iotrace_metrics_file="${BLK_IOTRACE_WORK}/${label}.metrics"
		blk_iotrace_scrape_started=$(date +%s.%N)
		huatuo_bamai_metrics > "${blk_iotrace_metrics_file}" || fatal "BlkIOTrace scrape failed: ${label}"
		blk_iotrace_scrape_ended=$(date +%s.%N)
	}

	assert_blk_iotrace() {
		assert_blk_iotrace_metrics "${blk_iotrace_disk}" "$1" "$2" \
			"${blk_iotrace_metrics_file}" "${blk_iotrace_scrape_started}" "${blk_iotrace_scrape_ended}" "${BLK_IOTRACE_WORK}/last-interval"
	}

	assert_blk_iotrace_active() {
		local operation=$1 label=$2
		assert_blk_iotrace "${operation}" active \
			|| fatal "BlkIOTrace public contract failed after ${label}; inspect ${blk_iotrace_metrics_file}"
	}

	assert_blk_iotrace_boundary() {
		local operation=$1 label=$2 status=0
		scrape_blk_iotrace "${label}"
		assert_blk_iotrace "${operation}" drain || status=$?
		((status == 0 || status == 3)) \
			|| fatal "invalid BlkIOTrace boundary ${label}: expected status 0 or 3, actual=${status}; inspect ${blk_iotrace_metrics_file}.assertion.json"
	}

	assert_blk_iotrace_workload() {
		local operation=$1 size=$2
		jq -e --arg disk "${blk_iotrace_disk}" --arg operation "${operation}" --argjson size "${size}" '
			[.[] | select(.labels.disk == $disk and
				(.name | startswith("huatuo_bamai_blk_iotrace_")) and
				(.name | startswith("huatuo_bamai_blk_iotrace_container_") | not))] as $host |
			all($host[] | select(.labels.operation != $operation); .value == 0) and
			($size != 4096 or all(["queued", "issued"][]; . as $kind |
				[$host[] | select(.labels.operation == $operation and
					.name == ("huatuo_bamai_blk_iotrace_" + $kind + "_io_size_bytes_bucket"))] as $rows |
				[$rows[] | select(.labels.le == "4096") | .value] as $first |
				[$rows[] | select(.labels.le == "+Inf") | .value] as $total |
				($first | length) == 1 and $first == $total and $first[0] > 0))
		' "${blk_iotrace_metrics_file}.json" > /dev/null \
			|| fatal "${operation}/${size}: unexpected operation samples or 4 KiB size classification; inspect ${blk_iotrace_metrics_file}.json"
	}

	assert_blk_iotrace_partition_remap() {
		local target partition="${blk_iotrace_disk}1" original_device=${blk_iotrace_device}
		if ! command -v udevadm > /dev/null 2>&1; then
			log_info "NOT_RUN L05 partition remap: udevadm is unavailable"
			return
		fi
		wait_until 5 0.1 test -b "/dev/${partition}" \
			|| fatal "owned scsi_debug partition did not appear: /dev/${partition}"
		timeout --kill-after=1s 10s udevadm settle --timeout=8 \
			|| fatal "udev did not settle before partition-remap collection"
		for target in whole partition; do
			blk_iotrace_device=${original_device}
			[[ ${target} != partition ]] || blk_iotrace_device="/dev/${partition}"
			assert_blk_iotrace_boundary read "remap-${target}-boundary"
			blk_iotrace_workload read 4096 16
			cp "${BLK_IOTRACE_WORK}/read-4096.json" "${BLK_IOTRACE_WORK}/remap-${target}.fio.json"
			scrape_blk_iotrace "remap-${target}"
			assert_blk_iotrace_active read "L05 ${target} reads"
			assert_blk_iotrace_workload read 4096
			jq -e --arg partition "${partition}" \
				'all(.[]; .labels.disk != $partition)' "${blk_iotrace_metrics_file}.json" > /dev/null \
				|| fatal "partition remap created a separate physical-disk series: ${partition}"
		done
		blk_iotrace_device=${original_device}
		jq -ne --slurpfile whole "${BLK_IOTRACE_WORK}/remap-whole.metrics.assertion.json" \
			--slurpfile partition "${BLK_IOTRACE_WORK}/remap-partition.metrics.assertion.json" '
			all(["queued_io_size_bytes_bucket", "issued_io_size_bytes_bucket", "d2c_seconds_bucket"][];
				. as $name | $whole[0].host_sample_counts.read[$name] as $expected |
				$expected > 0 and $partition[0].host_sample_counts.read[$name] == $expected)
		' > /dev/null || {
			cat "${BLK_IOTRACE_WORK}/remap-"{whole,partition}".metrics.assertion.json" >&2
			fatal "partition remap changed completed-read sample counts relative to whole-disk IO"
		}
		log_info "L05: partition reads retain physical-disk attribution without duplicate queue or completion samples"
	}

	blk_iotrace_workload() {
		local operation=$1 size=$2 count=$3 time_output=${4:-} index=0 flush=0
		local before after output="${BLK_IOTRACE_WORK}/${operation}-${size}"
		read -ra before < "/sys/block/${blk_iotrace_disk}/stat"
		printf 'device=%s operation=%s size=%s before=%s\n' \
			"${blk_iotrace_device}" "${operation}" "${size}" "${before[*]}" > "${output}.diskstats.log"
		if [[ ${operation} == write ]]; then
			((io_test_disk_writable == 1)) || fatal "test disk is not authorized for writes: ${blk_iotrace_device}"
			assert_io_test_disk_identity "${blk_iotrace_disk}" \
				|| fatal "configured test disk identity changed: ${blk_iotrace_disk}"
			index=4
			flush=1
		fi
		timeout --kill-after=2 15 fio --name=blk_iotrace --filename="${blk_iotrace_device}" --rw="${operation}" \
			--bs="${size}" --size="$((size * count))" --number_ios="${count}" --offset=0 \
			--ioengine=sync --direct=1 --iodepth=1 --numjobs=1 --thread=1 --allow_file_create=0 \
			--end_fsync="${flush}" --zero_buffers=1 --eta=never --output-format=json --output="${output}.json" \
			> "${output}.log" 2>&1 || fatal "fio ${operation} failed: ${blk_iotrace_device} size=${size}; inspect ${output}.log and ${output}.json"
		assert_io_fio_workload "${output}.json" "${operation}" "${size}" "${count}"
		read -ra after < "/sys/block/${blk_iotrace_disk}/stat"
		printf 'after=%s read_delta=%s write_delta=%s\n' "${after[*]}" \
			"$((after[0] - before[0]))" "$((after[4] - before[4]))" >> "${output}.diskstats.log"
		assert_io_disk_activity "${operation}" "${blk_iotrace_device}" "${before[index]}" "${after[index]}"
		if [[ -n ${time_output} ]]; then
			# Exclude fio setup time from the externally observed delay comparison.
			jq -e --arg operation "${operation}" '{seconds: (.jobs[0][$operation].runtime / 1000)}' \
				"${output}.json" > "${time_output}" || fatal "invalid fio workload duration: ${output}.json"
		fi
	}

	blk_iotrace_disk_idle() {
		local reads writes
		read -r reads writes < "/sys/block/${blk_iotrace_disk}/inflight" || return 1
		((reads == 0 && writes == 0))
	}

	blk_iotrace_idle_windows_ready() {
		local before after previous_end=0 mode=drain status=0
		blk_iotrace_idle_attempt=$((blk_iotrace_idle_attempt + 1))
		[[ ! -r ${BLK_IOTRACE_WORK}/last-interval ]] || previous_end=$(< "${BLK_IOTRACE_WORK}/last-interval")
		read -r before < "/sys/block/${blk_iotrace_disk}/stat" || fatal "cannot read BlkIOTrace idle disk statistics: ${blk_iotrace_disk}"
		scrape_blk_iotrace "idle-${blk_iotrace_idle_attempt}"
		read -r after < "/sys/block/${blk_iotrace_disk}/stat" || fatal "cannot read BlkIOTrace idle disk statistics after scrape: ${blk_iotrace_disk}"
		printf 'attempt=%s confirmed_windows=%s previous_end=%s\nbefore=%s\nafter=%s\n' \
			"${blk_iotrace_idle_attempt}" "${blk_iotrace_idle_intervals}" "${previous_end}" "${before}" "${after}" \
			> "${BLK_IOTRACE_WORK}/idle-observation.txt"
		if [[ ${before} == "${after}" ]] && blk_iotrace_disk_idle \
			&& awk -v previous="${previous_end}" '
			$1 ~ /^huatuo_bamai_blk_iotrace_interval_end_timestamp_seconds(\{|$)/ && $2 + 0 > previous { fresh = 1 }
			END { exit !fresh }
		' "${blk_iotrace_metrics_file}"; then
			mode=idle
		fi
		assert_blk_iotrace read "${mode}" || status=$?
		((status == 0 || status == 3)) \
			|| fatal "BlkIOTrace ${mode} interval contract failed; inspect ${BLK_IOTRACE_WORK}/idle-observation.txt and ${blk_iotrace_metrics_file}.assertion.json"
		if [[ ${mode} == idle && ${status} == 0 ]]; then blk_iotrace_idle_intervals=$((blk_iotrace_idle_intervals + 1)); fi
		((blk_iotrace_idle_intervals >= 2))
	}

	blk_iotrace_delay_owned() {
		local owner
		((io_test_scsi_owned)) || return 1
		read -r owner < /sys/module/scsi_debug/parameters/inq_vendor || return 1
		[[ ${owner} == "${io_test_scsi_owner}" ]] || return 1
		read -r owner < "/sys/block/${blk_iotrace_disk}/device/vendor" || return 1
		[[ ${owner} == "${io_test_scsi_owner}" ]]
	}

	blk_iotrace_delay_is_set() {
		local parameter=$1 value=$2 actual=unavailable
		if ! blk_iotrace_delay_owned; then
			printf '%s requested=%s state=owner_changed\n' "${parameter}" "${value}"
			return 1
		fi
		if ! blk_iotrace_disk_idle; then
			printf '%s requested=%s state=disk_busy\n' "${parameter}" "${value}"
			return 1
		fi
		# The public interface can reject changes while responses are pending.
		timeout --kill-after=1 1 bash -c 'printf "%s\n" "$2" > "$1"' \
			_ "${BLK_IOTRACE_DELAY_DIR}/${parameter}" "${value}" || return 1
		read -r actual < "${BLK_IOTRACE_DELAY_DIR}/${parameter}" || actual=unavailable
		printf '%s requested=%s actual=%s\n' "${parameter}" "${value}" "${actual}"
		[[ ${actual} == "${value}" ]]
	}

	set_blk_iotrace_delay() {
		wait_until 2 0.1 blk_iotrace_delay_is_set "$1" "$2" >> "${BLK_IOTRACE_WORK}/delay-control.log" 2>&1
	}

	restore_blk_iotrace_delay() {
		local actual_delay actual_ndelay
		((blk_iotrace_delay_restore_pending)) || return 0
		if ((blk_iotrace_original_ndelay > 0)); then
			set_blk_iotrace_delay ndelay "${blk_iotrace_original_ndelay}" || return 1
		else
			set_blk_iotrace_delay ndelay 0 || return 1
			set_blk_iotrace_delay delay "${blk_iotrace_original_delay}" || return 1
		fi
		read -r actual_delay < "${BLK_IOTRACE_DELAY_DIR}/delay" || return 1
		read -r actual_ndelay < "${BLK_IOTRACE_DELAY_DIR}/ndelay" || return 1
		[[ ${actual_delay} == "${blk_iotrace_original_delay}" && ${actual_ndelay} == "${blk_iotrace_original_ndelay}" ]] || return 1
		blk_iotrace_delay_restore_pending=0
		printf 'restored_delay=%s restored_ndelay=%s\n' "${actual_delay}" "${actual_ndelay}" \
			>> "${BLK_IOTRACE_WORK}/delay-resources.txt"
	}

	assert_blk_iotrace_delay_response() {
		local round phase nanoseconds label status confirmed=1
		local -a profiles
		if ((io_test_scsi_owned == 0)); then
			log_info "NOT_RUN L04: response-delay controls require an exclusively owned scsi_debug module"
			return 0
		fi
		# Public driver controls: https://sg.danny.cz/sg/scsi_debug.html
		if [[ ! -r ${BLK_IOTRACE_DELAY_DIR}/ndelay || ! -w ${BLK_IOTRACE_DELAY_DIR}/ndelay ||
			! -r ${BLK_IOTRACE_DELAY_DIR}/delay || ! -w ${BLK_IOTRACE_DELAY_DIR}/delay ]]; then
			log_info "NOT_RUN L04: writable scsi_debug response-delay controls are unavailable"
			return 0
		fi
		blk_iotrace_delay_owned || fatal "scsi_debug ownership changed before the delay comparison"
		read -r blk_iotrace_original_delay < "${BLK_IOTRACE_DELAY_DIR}/delay"
		read -r blk_iotrace_original_ndelay < "${BLK_IOTRACE_DELAY_DIR}/ndelay"
		[[ ${blk_iotrace_original_delay} =~ ^-?[0-9]+$ && ${blk_iotrace_original_ndelay} =~ ^[0-9]+$ ]] \
			|| fatal "invalid public scsi_debug delay values"
		printf 'owner=%s device=%s original_delay=%s original_ndelay=%s\n' \
			"${io_test_scsi_owner}" "${blk_iotrace_device}" "${blk_iotrace_original_delay}" "${blk_iotrace_original_ndelay}" \
			> "${BLK_IOTRACE_WORK}/delay-resources.txt"
		blk_iotrace_delay_restore_pending=1
		for round in 1 2; do
			profiles=()
			for phase in baseline delayed restored; do
				nanoseconds=3000000
				[[ ${phase} != delayed ]] || nanoseconds=30000000
				if ! set_blk_iotrace_delay ndelay "${nanoseconds}"; then
					log_info "NOT_RUN L04: response-delay control could not be applied and read back"
					confirmed=0
					break
				fi
				label="delay-${round}-${phase}"
				scrape_blk_iotrace "${label}-boundary"
				status=0
				assert_blk_iotrace read drain || status=$?
				((status == 0 || status == 3)) || fatal "BlkIOTrace contract failed at a delay-control boundary"
				blk_iotrace_workload read 4096 32 "${BLK_IOTRACE_WORK}/${label}.time.json"
				cp "${BLK_IOTRACE_WORK}/read-4096.log" "${BLK_IOTRACE_WORK}/${label}.log"
				scrape_blk_iotrace "${label}"
				assert_blk_iotrace_active read "L04 ${phase} reads"
				profiles+=("${BLK_IOTRACE_WORK}/${label}")
			done
			((confirmed)) || break
			status=0
			assert_blk_iotrace_delay_comparison "${blk_iotrace_disk}" "${profiles[@]}" \
				"${BLK_IOTRACE_WORK}/delay-${round}-comparison.json" || status=$?
			if ((status == 3)); then
				log_info "NOT_RUN L04: $(< "${BLK_IOTRACE_WORK}/delay-${round}-comparison.json")"
				confirmed=0
				break
			fi
			((status == 0)) || fatal "D2C distribution did not follow the independently confirmed response-delay change"
		done
		restore_blk_iotrace_delay || fatal "cannot restore scsi_debug response delay after L04"
		((confirmed == 0)) || log_info "L04: two externally qualified response-delay rounds shifted and restored the public D2C distribution"
	}

	io_test_start_bamai write_config_bamai_blk_iotrace_metrics \
		--region dev --disable-kubelet --disable-storage --log-debug
	if [[ ${scenario} == host ]]; then
		# Acquire after startup so the daemon cannot keep this phase's lock alive.
		# Udev probes stay outside the configured-disk capture windows.
		exec {blk_iotrace_disk_lock}< "${blk_iotrace_device}"
		flock --exclusive --timeout 5 "${blk_iotrace_disk_lock}" \
			|| fatal "cannot lock owned BlkIOTrace disk ${blk_iotrace_device}"
	fi

	blk_iotrace_warmup_ready() {
		local status=0
		blk_iotrace_warmup_attempt=$((blk_iotrace_warmup_attempt + 1))
		blk_iotrace_workload read 4096 64
		scrape_blk_iotrace "warmup-${blk_iotrace_warmup_attempt}"
		assert_blk_iotrace read warmup || status=$?
		((status != 0)) || return 0
		((status == 3)) || fatal "BlkIOTrace public contract failed during warmup"
		return 1
	}

	assert_blk_iotrace_ready() {
		local blk_iotrace_warmup_attempt=0
		wait_until 30 1 blk_iotrace_warmup_ready \
			|| fatal "completed reads did not produce mandatory BlkIOTrace distributions; inspect warmup metrics and huatuo.log"
	}
	assert_blk_iotrace_ready
	if [[ ${scenario} == partition ]]; then
		assert_blk_iotrace_partition_remap
		exit 0
	fi

	operations=(read write)
	for operation in "${operations[@]}"; do
		for workload in '4096 256' '65536 64' '1048576 16'; do
			read -r size count <<< "${workload}"
			assert_blk_iotrace_boundary "${operation}" "${operation}-${size}-boundary"
			blk_iotrace_workload "${operation}" "${size}" "${count}"
			scrape_blk_iotrace "${operation}-${size}"
			assert_blk_iotrace_active "${operation}" "L01 ${operation}/${size}"
			assert_blk_iotrace_workload "${operation}" "${size}"
		done
	done

	assert_blk_iotrace_boundary read idle-baseline
	blk_iotrace_idle_intervals=0 blk_iotrace_idle_attempt=0
	if wait_until 10 0.1 blk_iotrace_idle_windows_ready; then
		log_info "L02: no old histogram samples repeated in two healthy, independently quiet intervals"
	else
		log_info "NOT_RUN L02 strict idle: two healthy, advancing quiet intervals were not established; last observation: $(< "${BLK_IOTRACE_WORK}/idle-observation.txt")"
		log_info "L02 last interval: $(< "${blk_iotrace_metrics_file}.assertion.json")"
	fi
	blk_iotrace_workload read 4096 64
	scrape_blk_iotrace resumed-read
	assert_blk_iotrace_active read "L02 resumed reads"
	log_info "L02/L03: fixed buckets, public labels, interval timestamps and multiple input sizes verified"
	# Release the configured disk before SCSI preparation drains the udev queue.
	[[ -z ${blk_iotrace_disk_lock} ]] || exec {blk_iotrace_disk_lock}<&-
	blk_iotrace_disk_lock=""
	io_test_prepare_scsi_disks 1 1
	if ((io_test_scsi_owned)); then
		blk_iotrace_device="/dev/${io_test_disks[0]}"
		blk_iotrace_disk=${io_test_disks[0]}
		assert_blk_iotrace_ready
		assert_blk_iotrace_partition_remap
	else
		log_info "NOT_RUN L05 partition remap: no privately owned scsi_debug module"
	fi
	assert_blk_iotrace_delay_response
	exit 0
}

case ${TEST_BLK_IOTRACE_SCOPE:-all} in
all) io_test_run_cases "$0" TEST_BLK_IOTRACE_SCOPE host ;;
host) run_host_latency ;;
partition) run_host_latency partition ;;
*) fatal "TEST_BLK_IOTRACE_SCOPE must be all, host or partition" ;;
esac
