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

# CPU overhead scenario.
#
# Measures how much extra wall-clock time a fixed CPU-bound workload consumes
# while huatuo-bamai is collecting, under the realistic "full" config and the
# maximally reduced "minimal" config. Also records huatuo-bamai's own CPU
# consumption during the full-profile sampling window.

scenario_cpu() {
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

	local workdir conf_min items_min
	workdir=$(mktemp -d /tmp/huatuo-bench-cpu.XXXXXX)
	bench_register_cleanup "${workdir}"
	items_min="${workdir}/bl-min.txt"
	bench_blacklist_all > "${items_min}"
	conf_min="${workdir}/bamai.conf"
	gen_bench_conf "${conf_min}" "${items_min}"

	# ---- baseline (huatuo off) ----
	log_info "cpu: baseline sampling"
	sample_workload cpu_sample "${workdir}/base.txt"

	# ---- full profile (shipped multi-collector config) ----
	log_info "cpu: full-profile sampling"
	local cpu0 cpu1 full_huatuo_cpu
	if ! huatuo_start "$(full_config_path)"; then
		huatuo_stop
		scenario_error "huatuo-bamai failed to start (full profile)"
		return 0
	fi
	cpu0=$(huatuo_cpu_seconds)
	sample_workload cpu_sample "${workdir}/full.txt"
	cpu1=$(huatuo_cpu_seconds)
	full_huatuo_cpu=$(awk -v a="${cpu0}" -v b="${cpu1}" 'BEGIN { printf "%.4f", b - a }')
	huatuo_stop

	# ---- minimal profile (every optional collector blacklisted) ----
	log_info "cpu: minimal-profile sampling"
	local min_huatuo_cpu cpu0m cpu1m
	if ! huatuo_start "${conf_min}"; then
		huatuo_stop
		scenario_error "huatuo-bamai failed to start (minimal profile)"
		return 0
	fi
	cpu0m=$(huatuo_cpu_seconds)
	sample_workload cpu_sample "${workdir}/minimal.txt"
	cpu1m=$(huatuo_cpu_seconds)
	min_huatuo_cpu=$(awk -v a="${cpu0m}" -v b="${cpu1m}" 'BEGIN { printf "%.4f", b - a }')
	huatuo_stop

	local full_obj minimal_obj
	full_obj=$(profile_result_json "${workdir}/base.txt" "${workdir}/full.txt" "\"huatuo_cpu_seconds\":${full_huatuo_cpu}")
	minimal_obj=$(profile_result_json "${workdir}/base.txt" "${workdir}/minimal.txt" "\"huatuo_cpu_seconds\":${min_huatuo_cpu}")

	result_obj ok \
		"\"description\":\"extra wall time of a CPU-bound workload while huatuo collects\"," \
		"\"unit\":\"seconds\"," \
		"\"profiles\":{\"full\":${full_obj},\"minimal\":${minimal_obj}}"
}
