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

set -euo pipefail
source "${ROOT_DIR}/integration/lib_observability.sh"

assert_memory_load_failure() {
	local expected=$1
	wait_until 15 .1 grep -Fq "required map" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		|| fatal "missing map error was not reported"
	grep -Fq "${expected}" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		|| fatal "error omitted the required map name"
	! grep -Fq 'panic:' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		|| fatal "wrong BPF object crashed the daemon"
	huatuo_bamai_collect_metrics
	assert_huatuo_bamai_shutdown_with_signal TERM
}

# Use production objects first; a malformed-object test alone misses load regressions.
assert_memory_host_metrics() {
	huatuo_bamai_collect_metrics
	local name
	for name in "${memory_metrics[@]}"; do
		awk -v name="huatuo_bamai_${name}" '
   $0 == "# TYPE " name " gauge" { typed=1 }
   index($1,name "{")==1 {
    rows++
    if ($0 !~ /host="[^"]+"/ || $0 !~ /region="integration"/ ||
        $0 ~ /container_/ || $2 !~ /^[0-9]+([.][0-9]+)?([eE][+-]?[0-9]+)?$/ || $2+0<0) bad=1
   }
   END {exit !typed || rows!=1 || bad}
  ' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" || return 1
	done
}

mkdir "${HUATUO_BAMAI_TEST_TMPDIR}/bpf"
for observability_component in memory_free memory_reclaim; do
	case "${observability_component}" in
	memory_free)
		memory_object=memory_free_compact.o
		memory_map=mm_free_compact_map
		memory_metrics=(memory_free_allocpages_stall memory_free_compaction_stall)
		;;
	memory_reclaim)
		memory_object=memory_reclaim.o
		memory_map=memory_cgroup_allocpages_stall
		memory_metrics=(memory_reclaim_directstall)
		;;
	esac
	integration_huatuo_bamai_start write_config_bamai_observability \
		--region integration --disable-kubelet --log-debug --bpf-dir "${ROOT_DIR}/_output/bpf"
	wait_until 15 .1 assert_memory_host_metrics || fatal "production ${observability_component} metrics missing or invalid"
	assert_huatuo_bamai_shutdown_with_signal TERM
	# A successful host fallback is supported on kernels without container hooks.
	memory_log="${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	if [[ ${observability_component} == memory_free ]]; then
		memory_warning='msg="container memory stalls unavailable; collecting host stalls only"'
		memory_fallbacks=$(grep -Ec "level=\"(warn|warning)\" ${memory_warning}" "${memory_log}" || true)
		[[ ${memory_fallbacks} -le 1 ]] || fatal "memory stalls repeatedly fell back"
		awk -v warning="${memory_warning}" '
 /level="(warn|warning)"/ && index($0,warning) {next}
 {print}
 ' "${memory_log}" > "${HUATUO_BAMAI_TEST_TMPDIR}/memory-checked.log"
		memory_log="${HUATUO_BAMAI_TEST_TMPDIR}/memory-checked.log"
	fi
	assert_daemon_logs "${memory_log}"
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/${observability_component}-production.log"
	# A valid ELF with unrelated maps must fail through the loader error path.
	cp "${ROOT_DIR}/_output/bpf/cgroup_css_sync.o" "${HUATUO_BAMAI_TEST_TMPDIR}/bpf/${memory_object}"
	integration_huatuo_bamai_start write_config_bamai_observability \
		--region integration --disable-kubelet --log-debug \
		--bpf-dir "${HUATUO_BAMAI_TEST_TMPDIR}/bpf"
	assert_memory_load_failure "${memory_map}"
done
