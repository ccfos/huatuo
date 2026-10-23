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

# Shared memory snapshot workloads for integration and E2E tests.
set -euo pipefail

# Shared runtime workloads use the a/p/q protocol in integration and E2E.
snapshot_build_runtime_fixtures() {
	if [[ ${MEMSNAP_OPTIONAL_RUNTIMES:-0} != 1 ]]; then
		require_commands python3 java javac gcc g++
	fi
	cp "${ROOT_DIR}/integration/testdata/test_memsnapshot.py" "${HUATUO_BAMAI_TEST_TMPDIR}/snapshot.py"
	cp "${ROOT_DIR}/integration/testdata/TestMemsnapshotHeap.java" "${HUATUO_BAMAI_TEST_TMPDIR}/HeapFixture.java"
	if command -v java > /dev/null && command -v javac > /dev/null; then
		javac -source 8 -target 8 "${HUATUO_BAMAI_TEST_TMPDIR}/HeapFixture.java"
	fi
	cp "${ROOT_DIR}/integration/testdata/test_memsnapshot.c" "${HUATUO_BAMAI_TEST_TMPDIR}/snapshot.c"
	if command -v gcc > /dev/null; then
		gcc -O0 -o "${HUATUO_BAMAI_TEST_TMPDIR}/c-snapshot" "${HUATUO_BAMAI_TEST_TMPDIR}/snapshot.c"
	fi
	if command -v g++ > /dev/null; then
		g++ -x c++ -O0 -o "${HUATUO_BAMAI_TEST_TMPDIR}/cpp-snapshot" "${HUATUO_BAMAI_TEST_TMPDIR}/snapshot.c"
	fi
}

# Completed events must survive restart while allowing later records to append.
assert_memsnapshot_event_prefix() {
	local before=$1 after=$2 message=$3
	head -c "$(wc -c < "${before}")" "${after}" | cmp - "${before}" || fatal "${message}"
}

assert_memsnapshot_logs() {
	local file=$1
	[[ -r ${file} ]] || fatal "snapshot log is not readable: ${file}"
	if grep -qiE 'level="?(warn|warning|error|panic|fatal)"?|"level":"(warn|warning|error|panic|fatal)"|panic:' "${file}"; then
		cat "${file}" >&2
		fatal "unexpected warning/error in ${file}"
	fi
}

# These long-running tracers increment hitcount when Start returns, not per event.
# A zero count after a trigger also detects an unexpected tracer restart.
assert_memsnapshot_tracing_metrics() {
	local tracer=$1 region=$2 enabled=$3
	local running=${4:-${enabled}}
	huatuo_bamai_collect_metrics
	awk -v tracer="${tracer}" -v region="${region}" -v enabled="${enabled}" -v expected_running="${running}" '
 function labels_ok(line) {
  return line ~ /[{,]host="[^"]+"/ && index(line, "region=\"" region "\"") > 0
 }
 $0 == "# TYPE huatuo_bamai_tracing_status_hitcount gauge" { hit_type=1 }
 $0 == "# TYPE huatuo_bamai_tracing_status_running gauge" { running_type=1 }
 index($0,"huatuo_bamai_tracing_status_hitcount{")==1 && $0 ~ ("[{,]tracing=\"" tracer "\"") {
  hits++
  if (!labels_ok($0) || $NF !~ /^[0-9]+$/ || $NF+0 != 0) bad=1
 }
 index($0,"huatuo_bamai_tracing_status_running{")==1 {
  running++
  if (!labels_ok($0) || $NF !~ /^[0-9]+$/ || $NF+0 != expected_running) bad=1
 }
 END { exit bad || hits != enabled || running != 1 || !running_type || (enabled && !hit_type) }
 ' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" || {
		cat "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" >&2
		fatal "invalid tracing metrics for ${tracer}: expected enabled=${enabled}, running=${running}, hitcount=0"
	}
}

# Ignore samples before the input transition; health alone cannot prove sampling.
memsnapshot_memburst_samples_ready() {
	local offset=$1 minimum=$2 active_anon=${3:-}
	awk -v offset="${offset}" -v minimum="${minimum}" -v active="${active_anon}" '
 NR > offset && index($0, "Checked memory status.") &&
 (active == "" || index($0, "active_anon=" active " KiB")) { samples++ }
 END { exit samples < minimum }
 ' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
}

memsnapshot_wait_memburst_samples() {
	# Memburst scenarios sample once per second; allow a full 60-sample history.
	wait_until "$(($2 + 30))" 0.1 memsnapshot_memburst_samples_ready "$@" || fatal "memburst did not complete the expected samples"
}

# Build the shared test-only remote-read barrier source.
snapshot_build_read_barrier() {
	memsnapshot_read_source="${MEMSNAP_ACCEPTANCE_ARTIFACTS}/read-barrier.go"
	awk '
 /^import \(/ { print; print "\"strconv\""; next }
 /^func ProcessVMReadv\(/ { sub("ProcessVMReadv", "integrationProcessVMReadv") }
 { print }
' "${ROOT_DIR}/vendor/golang.org/x/sys/unix/zsyscall_linux.go" > "${memsnapshot_read_source}"
	cat >> "${memsnapshot_read_source}" << 'GO'

func integrationReadMark(path string) error {
 fd, err := Open(path, O_WRONLY|O_CREAT|O_EXCL|O_CLOEXEC, 0600)
 if err != nil { return err }
 return Close(fd)
}

func ProcessVMReadv(pid int, local []Iovec, remote []RemoteIovec, flags uint) (int, error) {
 dir, _ := syscall.Getenv("MEMSNAP_READ_BARRIER_DIR")
 target, _ := syscall.Getenv("MEMSNAP_READ_BARRIER_PID")
 active := dir != "" && target == strconv.Itoa(pid)
 if active && Access(dir+"/first-read", F_OK) == nil {
  if err := integrationReadMark(dir+"/reached"); err == nil {
   released := false
   for i:=0; i<6000; i++ {
    if Access(dir+"/release", F_OK) == nil { released=true; break }
    pause:=Timespec{Nsec:10000000}
    _ = Nanosleep(&pause,nil)
   }
   if !released { _ = integrationReadMark(dir+"/expired"); return 0, ETIMEDOUT }
  }
 }
 n, err := integrationProcessVMReadv(pid,local,remote,flags)
 if active {
  if n>0 && err==nil { _ = integrationReadMark(dir+"/first-read") }
  if err==ESRCH { _ = integrationReadMark(dir+"/target-exited") }
 }
 return n,err
}
GO
}

snapshot_read_barrier_ready() {
	[[ -f ${MEMSNAP_READ_BARRIER_DIR}/reached && ! -f ${MEMSNAP_READ_BARRIER_DIR}/expired ]]
}

# Capture the live scene before cleanup removes the Pod, process, or watches.
# Diagnostics are best effort and must never replace the original test status.
memsnapshot_dump_failure_diagnostics() (
	set +e
	local workspace=$1 cgroup=$2 workload_pid=$3 namespace=$4 pod=$5 stage=$6
	local file pid daemon_pid=""
	log_error "SNAPSHOT FAILURE: stage=${stage} pod=${namespace}/${pod} cgroup=${cgroup} workload_pid=${workload_pid} workspace=${workspace}"
	date -u '+%Y-%m-%dT%H:%M:%S.%NZ' >&2
	log_error '----- DISK CAPACITY AND INODES -----'
	df -h / /tmp /var/lib/containerd /var/lib/kubelet >&2
	df -i / /tmp /var/lib/containerd /var/lib/kubelet >&2
	log_error '----- NODE CONDITIONS, TAINTS AND POD EVENTS -----'
	timeout 12s kubectl --request-timeout=10s get nodes -o wide >&2
	timeout 12s kubectl --request-timeout=10s get nodes -o json | jq '.items[] | {name:.metadata.name,taints:.spec.taints,conditions:.status.conditions,capacity:.status.capacity,allocatable:.status.allocatable}' >&2
	timeout 12s kubectl --request-timeout=10s get pod -n "${namespace}" "${pod}" -o yaml >&2
	timeout 12s kubectl --request-timeout=10s describe pod -n "${namespace}" "${pod}" >&2
	timeout 12s kubectl --request-timeout=10s get events -A --sort-by=.metadata.creationTimestamp >&2
	log_error '----- CGROUP FILES (BEFORE CLEANUP) -----'
	findmnt -t cgroup,cgroup2 >&2
	for file in memory.current memory.max memory.high memory.events memory.events.local memory.usage_in_bytes memory.limit_in_bytes memory.failcnt memory.oom_control memory.stat cgroup.procs; do
		[[ -r "${cgroup}/${file}" ]] || continue
		printf '\n--- %s ---\n' "${cgroup}/${file}" >&2
		cat "${cgroup}/${file}" >&2
	done
	[[ ! -r "${workspace}/huatuo-bamai.pid" ]] || read -r daemon_pid < "${workspace}/huatuo-bamai.pid"
	for pid in "${daemon_pid}" "${workload_pid}"; do
		[[ ${pid} =~ ^[0-9]+$ ]] || continue
		log_error "----- LIVE PROCESS ${pid}: STATUS, CGROUP AND WATCHES -----"
		ps -p "${pid}" -o pid,ppid,stat,etime,args >&2
		for file in status cgroup; do
			[[ ! -r /proc/${pid}/${file} ]] || cat "/proc/${pid}/${file}" >&2
		done
		for file in /proc/"${pid}"/fdinfo/*; do
			[[ -r ${file} ]] || continue
			grep -H -E '^(inotify|eventfd|mnt_id|ino):|^inotify ' "${file}" >&2
		done
	done
	log_error '----- SNAPSHOT LOGS: ALL MATCHING LINES, THEN LAST 200 LINES -----'
	for file in "${workspace}"/huatuo.log "${workspace}"/daemon-*.log; do
		[[ -f ${file} ]] || continue
		log_error "----- ${file}: snapshot/watch/error timeline (line numbers) -----"
		grep -niE 'memory threshold|memory snapshot|memory watch|snapshot|candidate|container view|level="?(warn|warning|error|panic|fatal)|panic:' "${file}" >&2
		log_error "----- ${file}: last 200 lines -----"
		tail -n 200 "${file}" >&2
	done
	log_error '----- WORKLOAD OUTPUT AND EVENT READ ERRORS -----'
	for file in "${workspace}"/workload*.log "${workspace}"/snapshot-read.log; do
		[[ ! -f ${file} ]] || {
			log_error "----- ${file} -----"
			cat "${file}" >&2
		}
	done
	log_error '----- PERSISTED EVENTS -----'
	if [[ -f ${workspace}/events/memory_threshold_snapshot ]]; then
		wc -lc "${workspace}/events/memory_threshold_snapshot" >&2
		jq -c '{container_id,started_timestamp,observed_timestamp,uploaded_timestamp,tracer_data:(.tracer_data | {cgroup_path,victim_pid,language,memory_current,memory_max,memory_usage_percent,snapshot:(.snapshot | {status,status_reason,duration_ms,output_truncated,entry_count:(.entries|length)})})}' "${workspace}/events/memory_threshold_snapshot" >&2
	else
		log_error 'memory_threshold_snapshot event file does not exist'
	fi
	log_error '----- CURRENT TRACING METRICS -----'
	timeout 5s curl --silent --show-error "${CURL_TIMEOUT[@]}" "${HUATUO_BAMAI_METRICS_API:-http://127.0.0.1:19704/metrics}" | grep -E 'tracing_status|memory_threshold|memburst' >&2
	log_error '----- RECENT KUBELET MESSAGES -----'
	timeout 5s journalctl -u kubelet --no-pager -n 100 >&2
	return 0
)

# The cooldown contract is elapsed time, measured from the observed event.
memsnapshot_cooldown_elapsed() {
	local observed=$1 duration=$2
	((SECONDS - observed >= duration))
}

assert_snapshot_runtime_payload() {
	local language=$1 object_bytes=0 python_version=""
	if [[ ${language} == python ]]; then
		object_bytes=$(sed -n "s/^object_bytes=//p" "${go_snapshot_case_dir}/workload.log")
		[[ ${object_bytes} =~ ^[1-9][0-9]*$ ]] || fatal "Python fixture did not report object size"
	fi
	# CPython 3.6 predates _PyRuntime; newer interpreters must expose it.
	if [[ ${language} == python ]]; then
		python_version=$(python3 -c 'import sys; print("%d.%d" % sys.version_info[:2])')
	fi
	# Accept only independently established capability gaps, never arbitrary errors.
	if [[ ${language} == python ]] && jq -e '
  .tracer_data.snapshot.status == "unavailable"
  and .tracer_data.snapshot.status_reason == "CPython runtime is unsupported: Py_Version and a versioned libpython name are unavailable"
 ' "${go_snapshot_case_dir}/snapshot.json" > /dev/null || {
		[[ ${language} == python && ${python_version} == 3.6 ]] && jq -e '
  .tracer_data.snapshot.status == "unavailable"
  and .tracer_data.snapshot.status_reason == "CPython runtime is unsupported: no mapped module exports _PyRuntime"
 ' "${go_snapshot_case_dir}/snapshot.json" > /dev/null
	}; then
		jq -e '
  .tracer_data.language == "python"
  and .tracer_data.process_memory.rss_bytes >= 8388608
  and (.tracer_data.snapshot.duration_ms | type == "number" and . >= 0)
  and ((.tracer_data.snapshot.entries // []) | length == 0)
 ' "${go_snapshot_case_dir}/snapshot.json" > /dev/null || fatal "invalid unsupported CPython payload"
		log_info "PASS: CPython unsupported-runtime result; object statistics unavailable on this runtime"
		return
	fi
	jq -e --arg language "${language}" --argjson object_bytes "${object_bytes}" '
  .tracer_data as $data | $data.snapshot as $s |
  $data.language == $language
  and $data.process_memory.rss_bytes >= 8388608
  and ($s.duration_ms | type == "number")
  and (if $language == "native" or $language == "cpp" then
   $s.status == "unavailable" and ($s.status_reason | length > 0)
   and (($s.entries // []) | length == 0)
  else
   ($s.status == "complete" or $s.status == "partial")
   and ($s.runtime_version | length > 0)
   and ($s.entries | length > 0 and length <= 100)
   and ([$s.entries[].bytes] == ([$s.entries[].bytes] | sort | reverse))
   and all($s.entries[]; (.name | length > 0) and (.kind | length > 0)
    and .objects > 0 and .average_bytes == (.bytes / .objects))
   and (if $language == "python" then
    any($s.entries[]; (.name | contains("MemsnapshotPayload")) and .objects == 20000 and .bytes == (20000 * $object_bytes) and .average_bytes == $object_bytes)
   else
    $s.status == "partial" and ($s.status_reason | contains("concurrently"))
    and any($s.entries[]; .name == "HeapFixture$Payload"
     and .objects > 0 and .objects <= 200000 and .bytes == (.objects * 80) and .average_bytes == 80)
    and any($s.entries[]; .name == "HeapFixture$Payload[]"
     and .objects == 6 and .bytes == 7200096 and .average_bytes == 1200016)
   end)
  end)
 ' "${go_snapshot_case_dir}/snapshot.json" > /dev/null || fatal "${language}: invalid persisted runtime payload"
}

assert_go_snapshot_statistics() {
	local mode=$1
	jq -e --arg mode "${mode}" '
		def allocation($bytes; $objects):
			.kind == "inuse_space_objects"
			and .name == "main.allocateBlock"
			and .bytes == $bytes
			and .objects == $objects
			and .average_bytes == ($bytes / $objects)
			and (.stack | type == "array" and length > 0)
			and all(.stack[]; type == "string" and length > 0);
		.tracer_data.snapshot as $snapshot |
		if $mode == "disabled" then
			$snapshot.status == "unavailable"
			and ($snapshot.status_reason | contains("MemProfileRate=0"))
			and (($snapshot.entries // []) | length == 0)
			and (($snapshot.output_truncated // false) == false)
		else
			$snapshot.status == "complete"
			and ($snapshot.status_reason // "") == ""
			and ($snapshot.entries[0] | allocation(16777216; 6))
			and ([$snapshot.entries[].bytes] == ([$snapshot.entries[].bytes] | sort | reverse))
			and ([$snapshot.entries[].stack[] | select(startswith("main.allocateReleased, "))] | length == 0)
			and (if $mode == "topk" then
				($snapshot.entries | length == 1) and $snapshot.output_truncated == true
			else
				($snapshot.entries[1] | allocation(6291456; 3))
				and ([$snapshot.entries[] | select(.name == "main.allocateBlock")] | length == 2)
			end)
		end
	' "${go_snapshot_case_dir}/snapshot.json" > /dev/null \
		|| fatal "${mode}: incorrect Go stack statistics: $(< "${go_snapshot_case_dir}/snapshot.json")"
}
