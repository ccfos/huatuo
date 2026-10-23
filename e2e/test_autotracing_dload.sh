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

# Exercise real cgroup taskstats, proc stacks and both tracing storage backends.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_cgroup.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
source "${ROOT_DIR}/e2e/lib.sh"
source "${ROOT_DIR}/e2e/lib_cpu_load.sh"
source "${ROOT_DIR}/e2e/lib_cpu_metrics.sh"

readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 5)
readonly DLOAD_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/dload"
readonly DLOAD_BIN=${DLOAD_HELPER_BIN:-${ROOT_DIR}/_output/tests/dload-reference}
readonly DLOAD_NS="${BUSINESS_POD_NS}"
readonly DLOAD_PREFIX="dload-${BASHPID}-${RANDOM}"
readonly DLOAD_LABEL="app=${DLOAD_PREFIX}"
readonly DLOAD_INDEX="huatuo_e2e_${DLOAD_PREFIX}"
readonly DLOAD_DM="huatuo-${DLOAD_PREFIX}"
readonly DLOAD_SECTORS=65536
dload_interval=1
dload_threshold=1
dload_cooldown=2
dload_debug=false
dload_disabled=false
dload_issues='[]'
dload_loop=""
dload_device_dir=""
dload_mapper_created=false
dload_mounted=false
dload_module_loaded=false

require_commands kubectl jq ss timeout findmnt
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
[[ ${EUID} -eq 0 ]] || skip "requires root for taskstats and test block devices"
mkdir -p "${DLOAD_DIR}"
chmod 755 "${DLOAD_DIR}"
uname -a > "${DLOAD_DIR}/kernel.txt"
"${HUATUO_BAMAI_BIN}" --version > "${DLOAD_DIR}/bamai-version.txt"

dload_delay() {
	# Suspend flushes pending delayed reads before replacing the test-only table.
	log_info "dload device: read_delay_ms=$1"
	timeout 15 dmsetup --noudevsync suspend --noflush --nolockfs "${DLOAD_DM}" \
		|| {
			log_error "cannot suspend test mapper ${DLOAD_DM}"
			return 1
		}
	timeout 15 dmsetup --noudevsync reload "${DLOAD_DM}" \
		--table "0 ${DLOAD_SECTORS} delay ${dload_loop} 0 $1 ${dload_loop} 0 0" \
		|| {
			log_error "cannot reload test mapper ${DLOAD_DM}"
			return 1
		}
	timeout 15 dmsetup --noudevsync resume "${DLOAD_DM}" \
		|| {
			log_error "cannot resume test mapper ${DLOAD_DM}"
			return 1
		}
}

dload_remove_mapper() {
	# Pod deletion can precede kubelet's release of the filesystem mount.
	timeout 3 dmsetup --noudevsync remove "${DLOAD_DM}" > "${DLOAD_DIR}/dm-remove.log" 2>&1
}

dload_cleanup() {
	local status=$?
	trap - EXIT
	cpu_metrics_stop_queries
	if [[ ${dload_mapper_created} == true ]]; then
		dload_delay 0 || status=1
	fi
	kubectl --request-timeout=30s delete pod -n "${DLOAD_NS}" -l "${DLOAD_LABEL}" \
		--ignore-not-found --timeout=30s || status=1
	huatuo_bamai_stop || status=1
	storage_dump_logs || status=1
	storage_stop || status=1
	if [[ ${dload_mounted} == true ]]; then
		if nsenter --mount=/proc/1/ns/mnt -- timeout 15 umount "${dload_device_dir}/disk"; then
			dload_mounted=false
		else
			status=1
		fi
	fi
	if [[ ${dload_mapper_created} == true ]]; then
		if wait_until 30 1 dload_remove_mapper; then
			dload_mapper_created=false
		else
			log_error "test mapper ${DLOAD_DM} remains busy; inspect ${DLOAD_DIR}/dm-remove.log and ${dload_device_dir}/disk"
			status=1
		fi
	fi
	if [[ -n ${dload_loop} && ${dload_mapper_created} == false ]]; then
		if losetup --detach "${dload_loop}"; then
			dload_loop=""
		else
			status=1
		fi
	fi
	if [[ ${dload_module_loaded} == true && ${dload_mapper_created} == false ]]; then
		modprobe -r dm_delay || status=1
	fi
	if [[ -n ${dload_device_dir} ]]; then
		if [[ ${dload_mounted} == false && ${dload_mapper_created} == false && -z ${dload_loop} ]]; then
			rm -f "${dload_device_dir}/disk.img" || status=1
			rmdir "${dload_device_dir}/disk" "${dload_device_dir}" || status=1
		else
			log_error "block-device cleanup incomplete; backing image retained at ${dload_device_dir}"
			status=1
		fi
	fi
	log_info "dload artifacts: ${DLOAD_DIR}; cleanup status=${status}"
	exit "${status}"
}
trap dload_cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT

cpu_metrics_prepare
if [[ $(stat -fc %T /sys/fs/cgroup) == cgroup2fs ]]; then
	DLOAD_MODE=unified
elif [[ $(stat -fc %T /sys/fs/cgroup/unified 2> /dev/null || true) == cgroup2fs ]]; then
	DLOAD_MODE=hybrid
else
	DLOAD_MODE=legacy
fi
# Match test workers without depending on kernel-specific stack frames.
DLOAD_FIXTURE_ISSUES='[["e2e-unmatched","(?m)^Comm: dload-unmatched\\tPid: [0-9]+$"],["e2e-dload","(?m)^Comm: stress-ng-hdd\\tPid: [0-9]+$"]]'
DLOAD_PORT=$(allocate_available_port) || fatal "cannot allocate dload metrics port"
DLOAD_KUBELET_PORT=${KUBELET_PODS_API##*:}
DLOAD_KUBELET_PORT=${DLOAD_KUBELET_PORT%%/*}
readonly DLOAD_MODE DLOAD_FIXTURE_ISSUES DLOAD_PORT DLOAD_KUBELET_PORT

if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid ]]; then
	huatuo_bamai_stop
	huatuo_bamai_log_check || fatal "runner baseline daemon has unexpected errors"
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${DLOAD_DIR}/baseline.log"
fi
HUATUO_BAMAI_ADDR="http://127.0.0.1:${DLOAD_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

dload_status() {
	local enabled=1
	[[ ${dload_disabled} != true ]] || enabled=0
	assert_cpu_tracing_status dload "${enabled}"
}

assert_dload_query() {
	local enabled=1
	[[ ${dload_disabled} != true ]] || enabled=0
	assert_cpu_tracing_metrics dload "${enabled}" "$1"
}

dload_concurrent_status() {
	cpu_metrics_query_batch "${dload_work}" 3 3 assert_dload_query \
		|| fatal "concurrent dload metrics queries failed; inspect query files"
	cpu_metrics_read "${dload_work}/metrics.json"
}

assert_dload_logs() {
	[[ -r ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log ]] \
		|| fatal "dload daemon log is not readable: ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	HUATUO_BAMAI_MATCH_KEYWORDS="${HUATUO_BAMAI_MATCH_KEYWORDS}|\"level\":\"(error|panic|fatal)\"" \
		huatuo_bamai_log_check || fatal "dload daemon logged an unexpected failure"
	# Kernel creation hints can precede kubelet metadata; discovery is awaited.
	# CSS sample loss requests a full refresh; discovery and dload data are checked.
	! grep -iE 'level="?warn|"level":"warn"' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		| grep -v 'creation is not yet reflected by kubelet' \
		| grep -vE 'msg="lost BPF perf event samples".*func="internal/pod\.cgroupCssEventSyncHandler\.func1"' \
		| grep -q . \
		|| fatal "dload daemon logged an unexpected warning"
}

dload_start() {
	write_config_dload_autotracing > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	huatuo_bamai_start --config-dir "${HUATUO_BAMAI_TEST_TMPDIR}" --config bamai.conf --region e2e
	wait_until 10 0.5 dload_status || fatal "dload lifecycle metrics are incorrect"
}

dload_stop() {
	huatuo_bamai_stop_with_signal TERM
	assert_dload_logs
}

# Constructor validation must fail before any Pod or IO workload is created.
assert_dload_invalid_config() (
	local name=$1 expected=$2 status=0
	HUATUO_BAMAI_TEST_TMPDIR="${DLOAD_DIR}/invalid-${name}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	case ${name} in
	interval) dload_interval=-1 ;;
	threshold) dload_threshold=-1 ;;
	cooldown) dload_cooldown=-1 ;;
	esac
	write_config_dload_autotracing > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	timeout 15 "${HUATUO_BAMAI_BIN}" --region e2e --disable-kubelet \
		--config-dir "${HUATUO_BAMAI_TEST_TMPDIR}" --config bamai.conf \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/expected-error.log" 2>&1 || status=$?
	((status != 0 && status != 124 && status != 137)) || fatal "${name}: invalid config did not fail promptly"
	grep -Fq "${expected}" "${HUATUO_BAMAI_TEST_TMPDIR}/expected-error.log" \
		|| fatal "${name}: missing expected configuration error: ${expected}"
	log_info "dload: rejected negative ${name}"
)

assert_dload_invalid_config interval 'dload sampling interval must be positive'
assert_dload_invalid_config threshold 'dload threshold must be non-negative'
assert_dload_invalid_config cooldown 'dload tracing interval must be non-negative'

dload_unsupported_logged() {
	grep -Fq 'skipping dload tracing because cgroup v2 lacks load statistics' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
}

assert_dload_unsupported() {
	[[ ! -s ${HUATUO_BAMAI_TEST_TMPDIR}/events/dload ]] && dload_status
}

if [[ ${DLOAD_MODE} != legacy ]]; then
	HUATUO_BAMAI_TEST_TMPDIR="${DLOAD_DIR}/unsupported"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	dload_start
	wait_until 10 0.5 dload_unsupported_logged || fatal "${DLOAD_MODE}: missing unsupported-mode log"
	cpu_metrics_observe "$((dload_interval * 2))" 0.5 assert_dload_unsupported \
		|| fatal "${DLOAD_MODE}: unsupported mode saved events or changed lifecycle status"
	dload_stop
	skip "dload unsupported-mode behavior verified; functional/ES cases require Legacy cgroup (actual=${DLOAD_MODE})"
fi

require_commands docker crictl nsenter dmsetup losetup mkfs.ext4 modprobe truncate mount umount
docker info > "${DLOAD_DIR}/docker-info.log" 2>&1 || skip "Docker is unavailable for the ES upload test"
if [[ ! -d /sys/module/dm_delay ]]; then
	modprobe dm_delay > "${DLOAD_DIR}/dm-delay-module.log" 2>&1 || skip "dm-delay target is unavailable"
	dload_module_loaded=true
fi
dmsetup targets > "${DLOAD_DIR}/dm-targets.txt"
grep -q '^delay ' "${DLOAD_DIR}/dm-targets.txt" || skip "device-mapper delay target is unavailable"

cpu_load_prepare_image
DLOAD_CPU_ROOT=$(findmnt -rn -t cgroup -O cpu -o TARGET) || skip "v1 CPU cgroup controller is not mounted"
DLOAD_NODE=$(kubelet_pods_json | jq -er '[.items[].spec.nodeName] | unique | if length == 1 then .[0] else error("cannot identify local node") end')
readonly DLOAD_CPU_ROOT DLOAD_NODE

dload_device_dir="${DLOAD_DIR}/device"
mkdir -p "${dload_device_dir}/disk"
truncate -s 32M "${dload_device_dir}/disk.img"
dload_loop=$(losetup --find --show "${dload_device_dir}/disk.img") || skip "no usable loop device"
dmsetup --noudevsync create "${DLOAD_DM}" --table "0 ${DLOAD_SECTORS} delay ${dload_loop} 0 0 ${dload_loop} 0 0"
dload_mapper_created=true
dmsetup mknodes "${DLOAD_DM}"
mkfs.ext4 -q -F "/dev/mapper/${DLOAD_DM}"
# The runner has a private mount namespace; kubelet must see the same filesystem.
nsenter --mount=/proc/1/ns/mnt -- mount -t ext4 -o nosuid,nodev,noatime \
	"/dev/mapper/${DLOAD_DM}" "${dload_device_dir}/disk"
dload_mounted=true
printf 'mapper=%s loop=%s mount=%s cgroup_mode=%s\n' "${DLOAD_DM}" "${dload_loop}" \
	"${dload_device_dir}/disk" "${DLOAD_MODE}" > "${DLOAD_DIR}/device.txt"

storage_start elasticsearch_v8
storage_request "${DLOAD_DIR}/index-created.json" -X PUT "${STORAGE_ADDR}/${DLOAD_INDEX}" -H 'Content-Type: application/json' \
	--data '{"settings":{"number_of_shards":1,"number_of_replicas":0}}'
assert_storage_index_created "${DLOAD_DIR}/index-created.json"

dload_pod_ready() {
	kubectl --request-timeout=5s logs -n "${DLOAD_NS}" "${dload_pod}" \
		| grep -Fxq 'ready'
}

dload_create_pod() {
	local suffix=$1
	dload_pod="${DLOAD_PREFIX}-${suffix}"
	dload_work="${HUATUO_BAMAI_TEST_TMPDIR}/${suffix}"
	mkdir -p "${dload_work}"
	chmod 755 "${HUATUO_BAMAI_TEST_TMPDIR}" "${dload_work}"
	jq -n --arg name "${dload_pod}" --arg ns "${DLOAD_NS}" --arg app "${DLOAD_PREFIX}" \
		--arg node "${DLOAD_NODE}" --arg image "${CPU_LOAD_IMAGE}" --arg work "${dload_work}" \
		--arg disk "${dload_device_dir}/disk" '
		{apiVersion: "v1", kind: "Pod", metadata: {name: $name, namespace: $ns, labels: {app: $app}},
		 spec: {nodeName: $node, restartPolicy: "Never", terminationGracePeriodSeconds: 2,
		   containers: [{name: "worker", image: $image, imagePullPolicy: "Never",
		     command: ["sh", "-ec", "
		       trap \"exit 0\" TERM INT
		       stress-ng --version
		       echo ready
		       while [ ! -e /work/go ]; do sleep 0.1; done
		       stress-ng --hdd 4 --hdd-bytes 4M --hdd-write-size 4K --hdd-opts direct \\
		         --temp-path /delayed --timeout 120s --abort --metrics-brief &
		       pid=$!
		       trap \"kill -TERM $pid; wait $pid; exit 0\" TERM INT
		       while [ ! -e /work/stop ]; do
		         kill -0 \"$pid\" || { wait \"$pid\"; echo \"stress-ng exited before stop\" >&2; exit 1; }
		         sleep 0.1
		       done
		       kill -TERM \"$pid\"
		       wait \"$pid\"
		       trap \"exit 0\" TERM INT
		       echo complete
		       sleep 600
		     "],
		     resources: {requests: {cpu: "100m", memory: "16Mi"}, limits: {cpu: "500m", memory: "64Mi"}},
		     volumeMounts: [{name: "work", mountPath: "/work", readOnly: true},
		       {name: "disk", mountPath: "/delayed"}]}],
		   volumes: [{name: "work", hostPath: {path: $work, type: "Directory"}},
		     {name: "disk", hostPath: {path: $disk, type: "Directory"}}]}}
	' > "${dload_work}/pod.json"
	kubectl --request-timeout=20s create -f "${dload_work}/pod.json"
	kubectl wait --for=condition=Ready pod -n "${DLOAD_NS}" "${dload_pod}" --timeout=90s
	wait_until 15 0.5 dload_pod_ready || fatal "stress-ng controller did not initialize in ${dload_pod}"
	assert_huatuo_bamai_containers_present "${DLOAD_NS}" "^${dload_pod}$" 1
	dload_id=$(kubelet_container_ids "${DLOAD_NS}" "^${dload_pod}$")
	dload_cgroup=$(cgroup_find_container "${DLOAD_CPU_ROOT}" "${dload_id}") || fatal "cannot find Pod CPU cgroup"
	printf 'pod=%s id=%s cgroup=%s\n' "${dload_pod}" "${dload_id}" "${dload_cgroup}" \
		| tee "${dload_work}/target.txt"
}

dload_snapshot() {
	"${DLOAD_BIN}" sample --cgroup "${dload_cgroup}" > "$1"
}

dload_local_events() {
	if [[ -s ${HUATUO_BAMAI_TEST_TMPDIR}/events/dload ]]; then
		jq -s --arg id "${dload_id}" --argjson debug "${dload_debug}" \
			'[.[] | select($debug or .container_id == $id)]' \
			"${HUATUO_BAMAI_TEST_TMPDIR}/events/dload"
	else
		printf '[]\n'
	fi
}

dload_local_ready() {
	dload_local_events > "${dload_work}/local.json" || return 1
	jq -e --argjson count "${1:-1}" 'length >= $count' "${dload_work}/local.json" > /dev/null
}

dload_debug_ready() {
	dload_local_events > "${dload_work}/local.json" || return 1
	jq -e --argjson cooldown "${dload_cooldown}" '
		def event_time:
			capture("^(?<seconds>[^.]+)(?<fraction>\\.[0-9]+)?Z$") |
			(.seconds + "Z" | fromdateiso8601) + ((.fraction // "0") | tonumber);
		any(.[]; .tracer_data.dload_avg <= .tracer_data.threshold)
		and (group_by(.container_id) | any(.[];
			map(.started_timestamp | event_time) | sort |
			. as $times | any(range(1; length); . as $i |
				($times[$i] - $times[$i - 1]) as $elapsed | $elapsed > 0 and $elapsed < $cooldown)))
	' "${dload_work}/local.json" > /dev/null 2> "${dload_work}/debug-check.err"
}

dload_remote_events() {
	local query
	if [[ ${dload_debug} == true ]]; then
		# Other phases share the index; only this instance's local IDs belong here.
		query=$(dload_local_events | jq '
			{size: 1000, query: {bool: {filter: [{term: {"tracer_name.keyword": "dload"}}, {ids: {values: map(.tracer_id)}}]}}}')
	else
		query=$(jq -n --arg id "${dload_id}" \
			'{size: 1000, query: {bool: {filter: [{term: {"tracer_name.keyword": "dload"}}, {term: {"container_id.keyword": $id}}]}}}')
	fi
	storage_request "${dload_work}/search.json" -X POST "${STORAGE_ADDR}/${DLOAD_INDEX}/_search" \
		-H 'Content-Type: application/json' --data "${query}" \
		|| return 1
	jq -e '._shards.failed == 0 and .timed_out == false and .hits.total.relation == "eq"
		and .hits.total.value == (.hits.hits | length)
		and all(.hits.hits[]; ._id == ._source.tracer_id)' "${dload_work}/search.json" > /dev/null || return 1
	jq '[.hits.hits[]._source]' "${dload_work}/search.json" > "${dload_work}/remote.json"
}

dload_remote_ready() {
	dload_remote_events || return 1
	jq -e 'length > 0' "${dload_work}/remote.json" > /dev/null
}

dload_log_events() {
	local phase=$1 line
	jq -r --arg phase "${phase}" '
		sort_by(.started_timestamp)[] |
		"dload event: phase=\($phase) pod=\(.container_hostname) tracer_id=\(.tracer_id) " +
		"started_at=\(.started_timestamp) nr_uninterruptible=\(.tracer_data.nr_uninterruptible) " +
		"dload_avg=\(.tracer_data.dload_avg) threshold=\(.tracer_data.threshold) " +
		"known_issue=\(.tracer_data.known_issue)"
	' "${dload_work}/local.json" | while IFS= read -r line; do
		log_info "${line}"
	done
}

dload_log_reference() {
	log_info "$(jq -r --arg phase "$1" --arg pod "${dload_pod}" '
		"dload reference: phase=\($phase) pod=\($pod) " +
		"nr_uninterruptible=\(.stats.nr_uninterruptible) " +
		"proc_D=\([.tasks[] | select(.state == "D")] | length)"
	' "${dload_work}/blocked.json")"
}

assert_dload_storage() {
	local phase=$1
	dload_local_events > "${dload_work}/local.json"
	dload_log_events "${phase}"
	storage_request "${dload_work}/refresh.json" -X POST "${STORAGE_ADDR}/${DLOAD_INDEX}/_refresh"
	jq -e '._shards.failed == 0' "${dload_work}/refresh.json" > /dev/null || fatal "ES refresh failed"
	dload_remote_events || fatal "cannot query dload events from ES; inspect search.json/search.json.err"
	jq -e --slurpfile remote "${dload_work}/remote.json" '
		(length == (map(.tracer_id) | unique | length))
		and (sort_by(.tracer_id) == ($remote[0] | sort_by(.tracer_id)))
	' "${dload_work}/local.json" > /dev/null || fatal "local/ES dload event IDs or contents differ"
	log_info "dload storage: phase=${phase} pods=$(jq -c 'map(.container_hostname) | unique' "${dload_work}/local.json") local=$(jq length "${dload_work}/local.json") ES=$(jq length "${dload_work}/remote.json")"
}

assert_dload_baseline() {
	local file=$1
	jq -e '.stats.nr_uninterruptible == 0 and (.tasks | length > 0)
		and all(.tasks[]; .state != "D")' "${file}" > /dev/null || {
		jq '{nr_uninterruptible: .stats.nr_uninterruptible, tasks: [.tasks[] | {tid, state}]}' "${file}" >&2
		fatal "expected a nonempty container with no D tasks at baseline; inspect ${file}"
	}
}

assert_dload_debug_idle() {
	local file=$1 count
	count=$(jq -er '.stats.nr_uninterruptible' "${file}")
	assert_eq "${count}" 0 "debug-mode D task count" \
		|| fatal "debug test unexpectedly generated D tasks: expected=0 actual=${count}; inspect ${file}"
}

assert_dload_event_count() {
	local phase=$1 expected=$2 count
	count=$(jq -er 'length' "${dload_work}/local.json")
	assert_eq "${count}" "${expected}" "${phase} event count" \
		|| fatal "${phase}: expected=${expected} events actual=${count}; inspect ${dload_work}/local.json"
}

assert_dload_events() {
	local issue=$1
	jq -e --arg id "${dload_id}" --arg pod "${dload_pod}" --arg ns "${DLOAD_NS}" \
		--arg host "$(hostname)" --arg issue "${issue}" --argjson threshold "${dload_threshold}" \
		--argjson debug "${dload_debug}" '
		def count: type == "number" and . >= 0 and floor == .;
		def text: type == "string" and length > 0;
		def timestamp: type == "string" and test("^[0-9]{4}-.*Z$");
		length > 0 and all(.[];
			.tracer_name == "dload" and .tracer_type == "autotracing"
			and (if $debug then
				[.container_id, .container_hostname, .container_host_namespace] | all(.[]; text)
			else .container_id == $id and .container_hostname == $pod and .container_host_namespace == $ns end)
			and .hostname == $host and .region == "e2e"
			and (.tracer_id | type == "string" and length > 0)
			and (.container_type | type == "string" and length > 0)
			and (.container_qos | type == "string" and length > 0)
			and (.started_timestamp | timestamp) and (.uploaded_timestamp | timestamp)
			and .uploaded_timestamp >= .started_timestamp
			and (.tracer_data |
				.threshold == $threshold
				and (if $debug then .known_issue | text else .known_issue == $issue end)
				and ([.nr_sleeping, .nr_running, .nr_stopped, .nr_uninterruptible, .nr_iowait] | all(.[]; count))
				and (.load_avg | type == "number" and . >= 0)
				and (.dload_avg | type == "number" and . >= 0)
				and (if $debug then true else .dload_avg > $threshold and .nr_uninterruptible > 0 end)
				and (.stack | if $debug then type == "string"
					else contains("stacktrace of D task in cgroup:") and contains("Comm: stress-ng-hdd") end)))
	' "${dload_work}/local.json" > /dev/null || fatal "invalid dload event metadata or data; inspect local.json"
	if [[ ${dload_debug} != true ]]; then
		jq -e --slurpfile blocked "${dload_work}/blocked.json" '
			any(.[]; .tracer_data.stack | split("stacktrace of D task in host:") |
				length == 2 and (.[1] as $host |
					any($blocked[0].tasks[] | select(.state == "D");
						.tid as $tid | $host | contains("Comm: stress-ng-hdd\tPid: \($tid)\n"))))
		' "${dload_work}/local.json" > /dev/null || fatal "no stable event captured a stress-ng worker in host stacks"
	fi
}

dload_blocked() {
	dload_snapshot "${dload_work}/blocked.json" || return 1
	jq -e '.stats.nr_uninterruptible == 4
		and ([.tasks[] | select(.state == "D")] | length == 4 and all(.[]; .stack | length > 0))' \
		"${dload_work}/blocked.json" > /dev/null
}

dload_complete() {
	kubectl --request-timeout=5s logs -n "${DLOAD_NS}" "${dload_pod}" > "${dload_work}/pod.log" || return 1
	grep -Fxq 'complete' "${dload_work}/pod.log"
}

dload_workers_gone() {
	local tid
	while read -r tid; do
		[[ ! -d /proc/${tid} ]] || return 1
	done < <(jq -r '.tasks[] | select(.state == "D") | .tid' "${dload_work}/blocked.json")
}

dload_reference_stop() {
	if [[ -n ${dload_reference_pid} ]]; then
		kill -TERM "${dload_reference_pid}" 2> /dev/null || true
		wait "${dload_reference_pid}" 2> /dev/null || true
		dload_reference_pid=""
	fi
}

dload_case_cleanup() {
	local status=$?
	trap - EXIT
	if [[ ${status} -ne 0 ]]; then
		# Preserve blocked tasks before releasing IO or deleting the Pod.
		if [[ -n ${dload_cgroup:-} ]]; then
			timeout 5 "${DLOAD_BIN}" sample --cgroup "${dload_cgroup}" \
				> "${HUATUO_BAMAI_TEST_TMPDIR}/tasks-failure.json" \
				2> "${HUATUO_BAMAI_TEST_TMPDIR}/tasks-failure.err" || true
		fi
		if [[ -n ${dload_pod:-} ]]; then
			kubectl --request-timeout=5s logs -n "${DLOAD_NS}" "${dload_pod}" \
				> "${HUATUO_BAMAI_TEST_TMPDIR}/pod-failure.log" 2>&1 || true
		fi
		kubectl --request-timeout=10s describe pod -n "${DLOAD_NS}" -l "${DLOAD_LABEL}" \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/pods-failure.txt" 2>&1 || true
	fi
	cpu_metrics_stop_queries
	dload_delay 0 || status=1
	dload_reference_stop
	kubectl --request-timeout=30s delete pod -n "${DLOAD_NS}" -l "${DLOAD_LABEL}" \
		--ignore-not-found --timeout=30s || status=1
	huatuo_bamai_stop || status=1
	if [[ ${status} -eq 0 ]]; then
		assert_dload_logs
	fi
	exit "${status}"
}

dload_new_remote_event() {
	dload_remote_events || return 1
	jq -e --slurpfile before "${dload_work}/before-restart.json" '
		map(.tracer_id) - ($before[0] | map(.tracer_id)) | length > 0
	' "${dload_work}/remote.json" > /dev/null
}

assert_dload_idle() {
	dload_snapshot "${dload_work}/idle-sample.json" || return 1
	jq -e '.stats.nr_uninterruptible == 0' "${dload_work}/idle-sample.json" > /dev/null || return 1
	dload_local_events > "${dload_work}/baseline-events.json" || return 1
	jq -e 'length == 0' "${dload_work}/baseline-events.json" > /dev/null || return 1
	dload_remote_events || return 1
	jq -e 'length == 0' "${dload_work}/remote.json" > /dev/null || return 1
	dload_status
}

dload_run_case() (
	local name=$1 issue=none pid started_ms old_id
	HUATUO_BAMAI_TEST_TMPDIR="${DLOAD_DIR}/${name}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	dload_reference_pid=""
	trap dload_case_cleanup EXIT
	trap 'exit 143' TERM
	trap 'exit 130' INT
	dload_threshold=1 dload_cooldown=2 dload_debug=false dload_disabled=false dload_issues='[]'
	case ${name} in
	high-threshold) dload_threshold=8 ;;
	cooldown | restart | kill-recovery) dload_cooldown=60 ;;
	debug)
		dload_threshold=8 dload_cooldown=60 dload_debug=true
		dload_issues=${DLOAD_FIXTURE_ISSUES}
		;;
	classified)
		dload_threshold=0 dload_cooldown=0
		dload_issues=${DLOAD_FIXTURE_ISSUES}
		issue=e2e-dload
		;;
	disabled) dload_disabled=true ;;
	esac
	log_info "dload phase=${name} threshold=${dload_threshold} cooldown=${dload_cooldown} debug=${dload_debug}"
	dload_start
	dload_create_pod "${name}"
	dload_snapshot "${dload_work}/baseline.json"
	assert_dload_baseline "${dload_work}/baseline.json"
	if [[ ${name} == debug ]]; then
		wait_until 30 0.5 dload_local_ready || fatal "debug mode did not save any container event"
		wait_until 30 0.5 dload_remote_ready || fatal "debug events were not uploaded while bamai was running"
		wait_until 20 0.5 dload_debug_ready \
			|| fatal "debug mode did not bypass threshold/cooldown; inspect ${dload_work}/debug-check.err"
		dload_snapshot "${dload_work}/idle-after.json"
		assert_dload_debug_idle "${dload_work}/idle-after.json"
		dload_concurrent_status
		dload_status
		dload_stop
		assert_dload_storage "${name}"
		assert_dload_events none
		dload_debug_ready || fatal "saved debug events did not bypass threshold/cooldown; inspect ${dload_work}/debug-check.err"
		return
	fi
	cpu_metrics_observe "$((dload_interval * 2))" 0.5 assert_dload_idle \
		|| fatal "idle container has D tasks, unexpected events or invalid lifecycle status"
	timeout 25 "${DLOAD_BIN}" sample --cgroup "${dload_cgroup}" --duration 18s \
		> "${dload_work}/samples.jsonl" 2> "${dload_work}/reference.err" &
	dload_reference_pid=$!
	dload_delay 1000
	dmsetup table "${DLOAD_DM}" > "${dload_work}/dm-table.txt"
	date -u '+%Y-%m-%dT%H:%M:%S.%NZ' > "${dload_work}/triggered-at.txt"
	touch "${dload_work}/go"
	wait_until 8 0.5 dload_blocked || fatal "direct IO did not produce four netlink-counted D tasks"
	dload_log_reference "${name}"
	if [[ ${name} != high-threshold && ${name} != disabled ]]; then
		wait_until 20 0.5 dload_local_ready || fatal "D tasks did not trigger dload"
		wait_until 30 0.5 dload_remote_ready || fatal "dload events were not uploaded while bamai was running"
	fi
	dload_concurrent_status
	if [[ ${name} == restart || ${name} == kill-recovery ]]; then
		cp "${dload_work}/remote.json" "${dload_work}/before-restart.json"
		pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
		if [[ ${name} == kill-recovery ]]; then
			kill -KILL "${pid}"
			if wait "${pid}"; then fatal "SIGKILL did not terminate bamai"; else
				[[ $? -eq 137 ]] || fatal "bamai did not exit from SIGKILL"
			fi
			huatuo_bamai_stop
			assert_dload_logs
		else
			dload_stop
		fi
		cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/before-restart.log"
		dload_start
		wait_until 20 0.5 dload_new_remote_event || fatal "restarted bamai did not resume dload uploads"
		jq -e --slurpfile before "${dload_work}/before-restart.json" \
			'($before[0] | map(.tracer_id)) - map(.tracer_id) | length == 0' \
			"${dload_work}/remote.json" > /dev/null || fatal "restart lost previously uploaded events"
	fi
	if [[ ${name} == pod-lifecycle ]]; then
		dload_reference_stop
		dload_blocked || fatal "stress-ng workers were no longer blocked before Pod deletion"
		old_id=${dload_id}
		cp "${dload_work}/remote.json" "${dload_work}/before-delete.json"
		started_ms=$(date +%s%3N)
		kubectl --request-timeout=30s delete pod -n "${DLOAD_NS}" "${dload_pod}" --timeout=20s \
			> "${dload_work}/delete.log" 2>&1
		printf 'deletion_elapsed_ms=%s\n' "$(($(date +%s%3N) - started_ms))" | tee "${dload_work}/deletion.txt"
		dload_workers_gone || fatal "stress-ng worker survived Pod deletion"
		assert_huatuo_bamai_containers_absent 'deleted dload container' "${old_id}"
		dload_delay 0
		assert_dload_storage "${name}"
		assert_dload_events none
		dload_create_pod replacement
		[[ ${dload_id} != "${old_id}" ]] || fatal "replacement Pod reused the old container ID"
		dload_delay 1000
		touch "${dload_work}/go"
		wait_until 8 0.5 dload_blocked || fatal "replacement stress-ng workers did not block"
		dload_log_reference "${name}"
		wait_until 20 0.5 dload_local_ready || fatal "replacement container did not trigger dload"
		wait_until 30 0.5 dload_remote_ready || fatal "replacement event did not reach ES"
	fi
	if [[ -n ${dload_reference_pid} ]]; then
		wait "${dload_reference_pid}" || fatal "reference sampling failed; inspect reference.err"
		dload_reference_pid=""
		jq -se 'any(.[]; .stats.nr_uninterruptible == 4 and ([.tasks[] | select(.state == "D")] | length == 4))' \
			"${dload_work}/samples.jsonl" > /dev/null || fatal "reference never observed four D tasks"
	fi
	# Release delayed reads before asking stress-ng to stop and reap its workers.
	dload_delay 0
	touch "${dload_work}/stop"
	wait_until 10 0.5 dload_complete \
		|| fatal "stress-ng workload did not stop successfully; inspect ${dload_work}/pod.log and ${HUATUO_BAMAI_TEST_TMPDIR}/tasks-failure.json"
	dload_workers_gone || fatal "stress-ng worker remains after workload completion"
	dload_status
	dload_stop
	assert_dload_storage "${name}"
	case ${name} in
	high-threshold | disabled)
		assert_dload_event_count "${name}" 0
		;;
	cooldown)
		assert_dload_event_count "${name}" 1
		assert_dload_events "${issue}"
		;;
	*) assert_dload_events "${issue}" ;;
	esac
)

for dload_case in positive high-threshold cooldown debug classified disabled restart kill-recovery pod-lifecycle; do
	dload_run_case "${dload_case}"
	log_info "PASS: dload ${dload_case}"
done
