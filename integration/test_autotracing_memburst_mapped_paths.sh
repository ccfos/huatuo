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

# Real Go PIE, HotSpot and CPython snapshots from copied paths in private namespaces.
set -euo pipefail
source "${ROOT_DIR}/integration/lib_memburst.sh"
require_commands go jq mount umount awk timeout python3 readelf readlink
# Each detector replaces /proc/meminfo and ranks processes by RSS. Keep both
# views private to its runtime group, and reserve ports through the parent worker.
if (($# == 0)); then
	declare -A burst_group_names=()
	for group in go python java; do
		workspace="${HUATUO_BAMAI_TEST_TMPDIR}/${group}"
		mkdir -p "${workspace}"
		HUATUO_BAMAI_TEST_TMPDIR="${workspace}" unshare --mount --propagation private \
			--pid --fork --kill-child=KILL --mount-proc bash "$0" "${group}" &
		burst_group_names[$!]=${group}
	done
	burst_wait_groups
	exit 0
fi
mount -t tmpfs tmpfs /var/run
unset HUATUO_BAMAI_DEFAULT_ADDR
huatuo_bamai_initialize_address
burst_setup
readonly MEMBURST_PIE_FIXTURE="${MEMSNAP_ACCEPTANCE_ARTIFACTS}/go-snapshot-pie"
trap burst_cleanup EXIT

# Use actual copied executables and libraries: symlinks can retain the original
# maps pathname, and changing a script or classpath does not move the runtime.
assert_burst_mapped_path() {
	local expected=$1
	cp "/proc/${burst_runtime_pid}/maps" "${HUATUO_BAMAI_TEST_TMPDIR}/maps"
	python3 - "${HUATUO_BAMAI_TEST_TMPDIR}/maps" << 'PYTHON' > "${HUATUO_BAMAI_TEST_TMPDIR}/mapped-paths.json"
import json
import sys

with open(sys.argv[1], "rb") as source:
    fields = (line.split(None, 5) for line in source.read().split(b"\n"))
    # The fixtures use UTF-8 even when Python starts with an ASCII locale.
    paths = [entry[5].decode("utf-8", "surrogateescape") for entry in fields if len(entry) == 6]
    print(json.dumps(paths))
PYTHON
	jq -e --arg expected "${expected}" 'index($expected) != null' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/mapped-paths.json" > /dev/null || {
		cat "${HUATUO_BAMAI_TEST_TMPDIR}/mapped-paths.json" >&2
		fatal "target ${burst_runtime_pid} does not map expected pathname: ${expected}"
	}
}

assert_burst_executable_path() {
	local expected=$1 actual
	actual=$(readlink "/proc/${burst_runtime_pid}/exe") || fatal "cannot read executable for ${burst_runtime_pid}"
	assert_eq "${actual}" "${expected}" "target executable path" || fatal "target started from an unexpected executable"
	assert_burst_mapped_path "${expected}"
}

assert_burst_mapped_jvm() {
	local java_home=$1
	assert_burst_executable_path "${java_home}/bin/java"
	# JDKs can ship test JVMs; only the library mapped by this target matters.
	jq -e --arg prefix "${java_home}/" '
 [ .[] | select(endswith("/libjvm.so")) ] | unique
 | length == 1 and all(.[]; startswith($prefix))
' "${HUATUO_BAMAI_TEST_TMPDIR}/mapped-paths.json" > /dev/null || {
		cat "${HUATUO_BAMAI_TEST_TMPDIR}/mapped-paths.json" >&2
		fatal "target ${burst_runtime_pid} must map one libjvm.so from copied JDK ${java_home}"
	}
}

assert_burst_mapped_snapshot() {
	local language=$1
	go_snapshot_case_dir=${HUATUO_BAMAI_TEST_TMPDIR}
	# Select this target and trigger, rather than accepting any persisted event.
	jq -se --argjson pid "${burst_runtime_pid}" --arg at "${memburst_path_triggered_at}" '
 [ .[] | select(.started_timestamp >= $at)
   | .tracer_data.process_snapshots[]? | select(.pid == $pid) ]
 | select(length == 1) | {tracer_data: .[0]}
' "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" > "${HUATUO_BAMAI_TEST_TMPDIR}/snapshot.json" || {
		cat "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" >&2
		fatal "${language}: expected one snapshot for target ${burst_runtime_pid} after ${memburst_path_triggered_at}"
	}
	log_info "mapped-path snapshot: $(< "${HUATUO_BAMAI_TEST_TMPDIR}/snapshot.json")"
	case ${language} in
	go) assert_go_snapshot_statistics go ;;
	python) assert_snapshot_runtime_payload python deadline-partial ;;
	java) assert_snapshot_runtime_payload java ;;
	esac
	# Payload helpers own census completeness; runtime discovery must still succeed.
	jq -e --arg language "${language}" '.tracer_data | .language == $language
 and (.snapshot | (.status == "complete" or .status == "partial")
 and (.runtime_version | length > 0))' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/snapshot.json" > /dev/null || fatal "${language}: mapped-path runtime discovery did not succeed"
}

burst_capture_mapped_path() {
	local language=$1
	burst_top=10 burst_threshold=10 burst_anon=0 burst_blacklist=0 burst_cooldown=1
	burst_input low
	burst_start
	burst_wait_samples low
	memburst_path_triggered_at=$(date -u '+%Y-%m-%dT%H:%M:%S.%NZ')
	burst_input high
	wait_until 30 .1 burst_expected_count 1 || fatal "${language}: mapped-path capture missing"
	assert_burst_mapped_snapshot "${language}"
	burst_stop TERM
	printf q >&"${burst_runtime_fd}"
	wait "${burst_runtime_pid}" || fatal "${language}: mapped-path workload failed"
	burst_workloads=()
	exec {burst_runtime_fd}>&-
	unset 'burst_fds[-1]'
}

burst_inspect_python() {
	"$1" - << 'PYTHON'
import json
import os
import re
import sys
import sysconfig

try:
    import ctypes
    api = ctypes.pythonapi
except ImportError:
    api = None

with open("/proc/self/maps", "rb") as source:
    fields = (line.split(None, 5) for line in source.read().split(b"\n"))
    paths = [entry[5].decode("utf-8", "surrogateescape") for entry in fields if len(entry) == 6]
print(json.dumps({
    "implementation": sys.implementation.name,
    "version": list(sys.version_info[:3]),
    "executable": os.path.realpath(sys.executable),
    "home": os.pathsep.join((sys.base_prefix, sys.base_exec_prefix)),
    "free_threaded": bool(int(sysconfig.get_config_var("Py_GIL_DISABLED") or 0)),
    "library": next((path for path in paths if os.path.basename(path).startswith("libpython")), ""),
    "exports_runtime": hasattr(api, "_PyRuntime"),
    "version_available": hasattr(api, "Py_Version") or any(re.search(r"/libpython3\.[0-9]+", path) for path in paths),
}))
PYTHON
}

burst_python_supported() {
	jq -e '.implementation == "cpython" and .version[0] == 3
 and .version[1] >= 8 and .version[1] <= 14
 and .free_threaded == false
 and .exports_runtime and .version_available' "$1" > /dev/null
}

assert_burst_pie_fixture() {
	grep -Eq 'Type:[[:space:]]+DYN' "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/pie-header.txt" \
		|| fatal "mapped-path Go fixture must be an ELF PIE (ET_DYN)"
}

mkdir -p "${MEMBURST_RUNTIME_DIR}"
case $1 in
go)
	go build -mod=vendor -buildmode=pie -o "${MEMBURST_PIE_FIXTURE}" ./integration/testdata/test_memory_threshold_snapshot_golang.go
	LC_ALL=C readelf -h "${MEMBURST_PIE_FIXTURE}" > "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/pie-header.txt"
	assert_burst_pie_fixture
	for memburst_path_case in ordinary consecutive_spaces tab unicode_space trailing_space trailing_cr; do
		HUATUO_BAMAI_TEST_TMPDIR="${MEMBURST_ROOT_WORKSPACE}/mapped-go-${memburst_path_case}"
		mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
		case ${memburst_path_case} in
		ordinary) memburst_path_name=heap ;;
		consecutive_spaces) memburst_path_name='heap  fixture' ;;
		tab) memburst_path_name=$'heap\tfixture' ;;
		unicode_space) memburst_path_name=$'heap\xc2\xa0fixture' ;;
		trailing_space) memburst_path_name='heap ' ;;
		trailing_cr) memburst_path_name=$'heap\r' ;;
		esac
		memburst_path_executable="${HUATUO_BAMAI_TEST_TMPDIR}/${memburst_path_name}"
		cp "${MEMBURST_PIE_FIXTURE}" "${memburst_path_executable}"
		burst_start_runtime go "${memburst_path_executable}"
		assert_burst_executable_path "${memburst_path_executable}"
		burst_capture_mapped_path go
		log_info "PASS: Go PIE mapped pathname ${memburst_path_case}"
	done
	exit 0
	;;
python)
	cp "${ROOT_DIR}/integration/testdata/test_memsnapshot.py" "${MEMBURST_RUNTIME_DIR}/snapshot.py"
	burst_inspect_python python3 > "${MEMBURST_RUNTIME_DIR}/python-runtime.json"
	memburst_python_venv_metadata=""
	if burst_python_supported "${MEMBURST_RUNTIME_DIR}/python-runtime.json"; then
		if python3 -c 'import venv' > /dev/null 2>&1; then
			python3 -m venv --without-pip "${MEMBURST_RUNTIME_DIR}/python-venv"
			memburst_python_venv_metadata="${MEMBURST_RUNTIME_DIR}/python-venv.json"
			burst_inspect_python "${MEMBURST_RUNTIME_DIR}/python-venv/bin/python3" > "${memburst_python_venv_metadata}"
		else
			log_info 'SKIP: Python venv mapped-path cases require the venv module'
		fi
	else
		log_info "SKIP: Python mapped-path cases require GIL-enabled CPython 3.8-3.14 with _PyRuntime and version metadata: $(< "${MEMBURST_RUNTIME_DIR}/python-runtime.json")"
	fi
	modes=(python python_venv)
	;;
java)
	HUATUO_BAMAI_TEST_TMPDIR="${MEMBURST_RUNTIME_DIR}" snapshot_build_managed_runtime_fixtures
	memburst_java_home=""
	if [[ -f ${MEMBURST_RUNTIME_DIR}/HeapFixture.class ]]; then
		memburst_java_executable=$(readlink -f "$(command -v java)")
		memburst_java_home="${MEMBURST_RUNTIME_DIR}/mapped-jdk"
		cp -a "${memburst_java_executable%/bin/java}" "${memburst_java_home}"
	else
		log_info 'SKIP: Java mapped-path cases require java and javac'
	fi
	modes=(java)
	;;
*) fatal "unknown mapped-path group: $1" ;;
esac
for memburst_path_case in ordinary consecutive_spaces tab; do
	case ${memburst_path_case} in
	ordinary) memburst_path_name=runtime ;;
	consecutive_spaces) memburst_path_name='runtime  name' ;;
	tab) memburst_path_name=$'runtime\tname' ;;
	esac
	for memburst_path_mode in "${modes[@]}"; do
		memburst_path_language=${memburst_path_mode%_venv}
		if [[ ${memburst_path_language} == java && -z ${memburst_java_home} ]]; then continue; fi
		if [[ ${memburst_path_language} == python ]]; then
			memburst_python_metadata="${MEMBURST_RUNTIME_DIR}/python-runtime.json"
			if [[ ${memburst_path_mode} == python_venv ]]; then
				[[ -n ${memburst_python_venv_metadata} ]] || continue
				memburst_python_metadata=${memburst_python_venv_metadata}
			fi
			burst_python_supported "${memburst_python_metadata}" || continue
			memburst_python_executable=$(jq -r .executable "${memburst_python_metadata}")
			memburst_python_home=$(jq -r .home "${memburst_python_metadata}")
			memburst_python_library=$(jq -r .library "${memburst_python_metadata}")
		fi
		HUATUO_BAMAI_TEST_TMPDIR="${MEMBURST_ROOT_WORKSPACE}/mapped-${memburst_path_mode}-${memburst_path_case}"
		go_snapshot_case_dir=${HUATUO_BAMAI_TEST_TMPDIR}
		memburst_path_runtime="${HUATUO_BAMAI_TEST_TMPDIR}/${memburst_path_name}"
		mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
		if [[ ${memburst_path_language} == python ]]; then
			mkdir -p "${memburst_path_runtime}/lib"
			cp "${memburst_python_executable}" "${memburst_path_runtime}/python3"
			if [[ -n ${memburst_python_library} ]]; then
				cp "${memburst_python_library}" "${memburst_path_runtime}/lib/"
			fi
			burst_start_runtime python "${memburst_path_runtime}/python3"
			assert_burst_executable_path "${memburst_path_runtime}/python3"
			if [[ -n ${memburst_python_library} ]]; then
				assert_burst_mapped_path "${memburst_path_runtime}/lib/${memburst_python_library##*/}"
			fi
		else
			mv "${memburst_java_home}" "${memburst_path_runtime}"
			memburst_java_home=${memburst_path_runtime}
			burst_start_runtime java "${memburst_java_home}/bin/java"
			assert_burst_mapped_jvm "${memburst_java_home}"
		fi
		burst_capture_mapped_path "${memburst_path_language}"
		log_info "PASS: ${memburst_path_mode} mapped pathname ${memburst_path_case}"
	done
done
