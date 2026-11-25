#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0

# Reject unsupported CPU selections before opening BPF resources.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"

readonly IRQ_CONFIG_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/irq-config.log"
readonly IRQ_CONFIG_OUTPUT="${HUATUO_BAMAI_TEST_TMPDIR}/profile.json"
require_commands timeout

run_irq_config_case() {
	local expected=$1 status=0
	shift
	timeout --kill-after=1 5 "${ROOT_DIR}/_output/bin/irqtracing" \
		--bpf-path "${ROOT_DIR}/_output/bpf/irqtracing.o" \
		--output-format json --output-path "${IRQ_CONFIG_OUTPUT}" \
		"$@" > "${IRQ_CONFIG_LOG}" 2>&1 || status=$?
	[[ ${status} -eq 1 ]] || fatal "IRQ configuration exit ${status}, expected 1: $*"
	grep -qF -- "${expected}" "${IRQ_CONFIG_LOG}" \
		|| fatal "expected '${expected}': $(cat "${IRQ_CONFIG_LOG}")"
	[[ ! -e "${IRQ_CONFIG_OUTPUT}" ]] || fatal "invalid IRQ selection created output"
}

run_irq_config_case 'flag provided but not defined: -target-cpu' --target-cpu 0
for cpu_list in '1,3-10' '1-3'; do
	run_irq_config_case 'supports only one target CPU' --cpu-list "${cpu_list}"
done
for cpu_list in '-1' '1,,2' '1, 2'; do
	run_irq_config_case 'invalid CPU ID' --cpu-list "${cpu_list}"
done
run_irq_config_case 'invalid CPU range' --cpu-list 3-1
run_irq_config_case 'not online' --cpu-list 2147483647
