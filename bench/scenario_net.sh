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

# Network latency scenario.
#
# Measures the incremental RTT huatuo-bamai adds to loopback traffic, under
# three profiles:
#   - full    : realistic multi-collector config (shipped huatuo-bamai.conf)
#   - minimal : every optional collector blacklisted
#   - single  : isolates ${BENCH_NET_SINGLE_MODULE} (default: dropwatch)

scenario_net() {
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
	command -v ping > /dev/null || {
		scenario_skipped "ping missing"
		return 0
	}

	local workdir items_min items_single conf_min conf_single
	workdir=$(mktemp -d /tmp/huatuo-bench-net.XXXXXX)
	bench_register_cleanup "${workdir}"
	items_min="${workdir}/bl-min.txt"
	items_single="${workdir}/bl-single.txt"
	bench_blacklist_all > "${items_min}"
	bench_blacklist_except "${BENCH_NET_SINGLE_MODULE}" > "${items_single}"
	conf_min="${workdir}/bamai-min.conf"
	conf_single="${workdir}/bamai-single.conf"
	gen_bench_conf "${conf_min}" "${items_min}"

	# single-module body: enable the isolated net module's event subsection.
	# "tcp" matches the filter shipped in huatuo-bamai.conf; raw IP protocol
	# keywords like "icmp" are rejected by the go-pcap filter compiler (see
	# the comment in huatuo-bamai.conf), so reusing the known-good value keeps
	# dropwatch's probes attached for the duration of the sample.
	local mod_title="${BENCH_NET_SINGLE_MODULE^}"
	cat > "${workdir}/${BENCH_NET_SINGLE_MODULE}.body" << EOF

[EventTracing.${mod_title}]
    Filter = "tcp"
EOF
	gen_bench_conf "${conf_single}" "${items_single}" "${workdir}/${BENCH_NET_SINGLE_MODULE}.body"

	# ---- baseline ----
	log_info "net: baseline sampling (${BENCH_NET_PACKETS} packets/sample)"
	sample_workload net_sample "${workdir}/base.txt"

	# ---- full ----
	log_info "net: full-profile sampling"
	if ! observed_samples "$(full_config_path)" net_sample "${workdir}/full.txt"; then
		scenario_error "huatuo-bamai failed to start (full profile)"
		return 0
	fi

	# ---- minimal ----
	log_info "net: minimal-profile sampling"
	if ! observed_samples "${conf_min}" net_sample "${workdir}/minimal.txt"; then
		scenario_error "huatuo-bamai failed to start (minimal profile)"
		return 0
	fi

	# ---- single ----
	log_info "net: single-profile sampling (${BENCH_NET_SINGLE_MODULE})"
	if ! observed_samples "${conf_single}" net_sample "${workdir}/single.txt"; then
		scenario_error "huatuo-bamai failed to start (single profile)"
		return 0
	fi

	local full_obj minimal_obj single_obj
	full_obj=$(profile_result_json "${workdir}/base.txt" "${workdir}/full.txt")
	minimal_obj=$(profile_result_json "${workdir}/base.txt" "${workdir}/minimal.txt")
	single_obj=$(profile_result_json "${workdir}/base.txt" "${workdir}/single.txt")

	result_obj ok \
		"\"description\":\"incremental loopback RTT while huatuo collects\"," \
		"\"unit\":\"microseconds\"," \
		"\"profiles\":{\"full\":${full_obj},\"minimal\":${minimal_obj},\"single\":${single_obj}}"
}
