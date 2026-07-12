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

# IO latency scenario.
#
# Measures the incremental per-operation latency huatuo-bamai adds to
# synchronized writes, under three profiles:
#   - full    : realistic multi-collector config (shipped huatuo-bamai.conf)
#   - minimal : every optional collector blacklisted
#   - single  : isolates ${BENCH_IO_SINGLE_MODULE} (default: iolatency)

scenario_io() {
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

	# io_sample needs a writable directory; prefer a real filesystem over tmpfs
	# so the kernel actually exercises the block-IO path huatuo instruments.
	local io_dir=""
	local mp
	while read -r mp; do
		[[ -d "${mp}" && -w "${mp}" ]] || continue
		io_dir=$(mktemp -d "${mp%/}/huatuo-bench-io.XXXXXX") && break
	done < <(awk '$3 == "ext4" || $3 == "xfs" { print $2 }' /proc/mounts)
	[[ -n "${io_dir}" ]] || {
		scenario_skipped "no writable ext4/xfs mount for io workload"
		return 0
	}

	local workdir items_min items_single conf_min conf_single
	workdir=$(mktemp -d /tmp/huatuo-bench-io-scenario.XXXXXX)
	bench_register_cleanup "${workdir}"
	# io_dir is created above; register it too so it is always removed.
	bench_register_cleanup "${io_dir}"
	items_min="${workdir}/bl-min.txt"
	items_single="${workdir}/bl-single.txt"
	bench_blacklist_all > "${items_min}"
	bench_blacklist_except "${BENCH_IO_SINGLE_MODULE}" > "${items_single}"
	conf_min="${workdir}/bamai-min.conf"
	conf_single="${workdir}/bamai-single.conf"
	gen_bench_conf "${conf_min}" "${items_min}"
	gen_bench_conf "${conf_single}" "${items_single}"

	# Override the io_sample work directory by exporting it for the workload.
	export BENCH_IO_DIR="${io_dir}"

	# ---- baseline ----
	log_info "io: baseline sampling (${BENCH_IO_OPS} sync writes/sample)"
	sample_workload io_sample "${workdir}/base.txt"

	# ---- full ----
	log_info "io: full-profile sampling"
	if ! observed_samples "$(full_config_path)" io_sample "${workdir}/full.txt"; then
		scenario_error "huatuo-bamai failed to start (full profile)"
		return 0
	fi

	# ---- minimal ----
	log_info "io: minimal-profile sampling"
	if ! observed_samples "${conf_min}" io_sample "${workdir}/minimal.txt"; then
		scenario_error "huatuo-bamai failed to start (minimal profile)"
		return 0
	fi

	# ---- single ----
	log_info "io: single-profile sampling (${BENCH_IO_SINGLE_MODULE})"
	if ! observed_samples "${conf_single}" io_sample "${workdir}/single.txt"; then
		scenario_error "huatuo-bamai failed to start (single profile)"
		return 0
	fi

	local full_obj minimal_obj single_obj
	full_obj=$(profile_result_json "${workdir}/base.txt" "${workdir}/full.txt")
	minimal_obj=$(profile_result_json "${workdir}/base.txt" "${workdir}/minimal.txt")
	single_obj=$(profile_result_json "${workdir}/base.txt" "${workdir}/single.txt")

	result_obj ok \
		"\"description\":\"incremental latency of synchronized writes while huatuo collects\"," \
		"\"unit\":\"milliseconds\"," \
		"\"profiles\":{\"full\":${full_obj},\"minimal\":${minimal_obj},\"single\":${single_obj}}"
}
