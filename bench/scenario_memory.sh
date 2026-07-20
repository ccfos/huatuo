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

# Memory overhead scenario.
#
# Measures the resident memory (RSS) of the huatuo-bamai process itself under a
# sustained mixed CPU+IO load. Absolute metric (no baseline). Reported for the
# realistic "full" config and the maximally reduced "minimal" config.

scenario_memory() {
	[[ ${EUID} -eq 0 ]] || {
		scenario_skipped "requires root"
		return 0
	}
	[[ -x "${HUATUO_BAMAI_BIN}" ]] || {
		scenario_skipped "huatuo-bamai binary missing"
		return 0
	}
	command -v curl > /dev/null || {
		scenario_skipped "curl missing"
		return 0
	}

	local workdir items_min conf_min
	workdir=$(mktemp -d /tmp/huatuo-bench-mem.XXXXXX)
	bench_register_cleanup "${workdir}"
	items_min="${workdir}/bl-min.txt"
	bench_blacklist_all > "${items_min}"
	conf_min="${workdir}/bamai.conf"
	gen_bench_conf "${conf_min}" "${items_min}"

	local mem_obj_full mem_obj_min

	# ---- full profile ----
	log_info "memory: full-profile sampling (window=${BENCH_MEM_DURATION}s)"
	if ! huatuo_start "$(full_config_path)"; then
		huatuo_stop
		scenario_error "huatuo-bamai failed to start (full profile)"
		return 0
	fi
	sustained_load "${BENCH_MEM_DURATION}" &
	local load_pid=$!
	sample_rss_window "${BENCH_MEM_DURATION}" "${workdir}/rss-full.txt"
	wait "${load_pid}" 2> /dev/null || true
	mem_obj_full=$(rss_summary_json "${workdir}/rss-full.txt")
	huatuo_stop

	# ---- minimal profile ----
	log_info "memory: minimal-profile sampling (window=${BENCH_MEM_DURATION}s)"
	if ! huatuo_start "${conf_min}"; then
		huatuo_stop
		scenario_error "huatuo-bamai failed to start (minimal profile)"
		return 0
	fi
	sustained_load "${BENCH_MEM_DURATION}" &
	load_pid=$!
	sample_rss_window "${BENCH_MEM_DURATION}" "${workdir}/rss-min.txt"
	wait "${load_pid}" 2> /dev/null || true
	mem_obj_min=$(rss_summary_json "${workdir}/rss-min.txt")
	huatuo_stop

	result_obj ok \
		"\"description\":\"huatuo-bamai resident memory under sustained load\"," \
		"\"unit\":\"MiB\"," \
		"\"window_seconds\":${BENCH_MEM_DURATION}," \
		"\"profiles\":{\"full\":${mem_obj_full},\"minimal\":${mem_obj_min}}"
}
