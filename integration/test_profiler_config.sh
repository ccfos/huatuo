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

# Verify CLI configuration errors are reported before BPF initialization.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"

readonly PROFILER_CONFIG_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly PROFILER_CONFIG_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/profiler-config.log"

run_profiler_config_case() {
	local expected=$1
	shift
	local status=0
	timeout --kill-after=1 5 "${PROFILER_CONFIG_BIN}" --type cpu --language c --pid "$$" \
		--output-format collapsed --output-path "${HUATUO_BAMAI_TEST_TMPDIR}/profile" --log-level info \
		"$@" > "${PROFILER_CONFIG_LOG}" 2>&1 || status=$?
	[[ ${status} -eq 1 ]] || fatal "expected configuration exit 1, got ${status}: $*"
	grep -qF -- "${expected}" "${PROFILER_CONFIG_LOG}" \
		|| fatal "expected '${expected}', got: $(cat "${PROFILER_CONFIG_LOG}")"
	if grep -qE 'init bpf|using profiler:|data reading loop started' "${PROFILER_CONFIG_LOG}"; then
		fatal "invalid configuration reached collection: $(cat "${PROFILER_CONFIG_LOG}")"
	fi
	[[ ! -e "${HUATUO_BAMAI_TEST_TMPDIR}/profile" ]] || fatal "invalid configuration created profile output"
}

require_commands timeout

run_profiler_config_case '--duration must be between 1 and 9223372036 seconds' --duration 9223372037
run_profiler_config_case 'exceeds duration' \
	--duration 9223372036 --aggr-interval 9223372037
for value in 0 -1; do
	run_profiler_config_case 'frequency must be at least 1' --freq "${value}"
	run_profiler_config_case 'duration must be at least 1' --duration "${value}"
	run_profiler_config_case 'aggregation interval must be at least 1' --aggr-interval "${value}"
done
run_profiler_config_case 'exceeds duration' --duration 1 --aggr-interval 2
run_profiler_config_case 'Incorrect Usage' --freq invalid
run_profiler_config_case 'invalid CPU ID' --cpu-list invalid
run_profiler_config_case 'invalid CPU range' --cpu-list 2-1
run_profiler_config_case 'not online' --cpu-list 2147483647
run_profiler_config_case 'invalid CPU ID' --cpu-list -1
run_profiler_config_case 'flag provided but not defined: -cpuid' --cpuid 0
for language in java python; do
	run_profiler_config_case 'CPU targeting is not supported' --language "${language}" --cpu-list 0
done
run_profiler_config_case 'CPU targeting is not supported' --type memory --memory-mode virtual_alloc --cpu-list 0

run_profiler_config_case 'invalid --pid' --pid '1,1'

run_profiler_config_case '--cpu-mode is valid only when --type=cpu' \
	--type memory --memory-mode virtual_alloc --cpu-mode oncpu
run_profiler_config_case 'unsupported CPU mode' --cpu-mode invalid
run_profiler_config_case '--memory-mode is only valid when --type=memory' --memory-mode virtual_alloc
run_profiler_config_case '--memory-mode is required' --type memory
run_profiler_config_case 'is not supported for c' --type memory --memory-mode object_alloc
run_profiler_config_case 'is not supported for java' --language java --type memory --memory-mode physical_alloc
run_profiler_config_case '--cpu-mode=offcpu is supported only by native CPU profiling' \
	--language java --cpu-mode offcpu
run_profiler_config_case '--freq is not used with --cpu-mode=offcpu' --cpu-mode offcpu --freq 99
run_profiler_config_case 'Java profiler frequency must not exceed 1000' --language java --freq 1001

# These errors must precede tool discovery, process attachment, and BPF setup.
for language in java python; do
	run_profiler_config_case '--pid and --container-id are mutually exclusive' \
		--language "${language}" --container-id aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
done
for language in c go c++; do
	run_profiler_config_case '--pid and --container-id are mutually exclusive' \
		--language "${language}" --type memory --memory-mode virtual_alloc \
		--container-id aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
done
for interval in 1 3; do
	run_profiler_config_case 'must equal --duration' \
		--language python --duration 2 --aggr-interval "${interval}"
done
for value in 0 -1; do
	run_profiler_config_case 'duration must be at least 1' \
		--language python --tool-path "${HUATUO_BAMAI_TEST_TMPDIR}" --duration "${value}" --aggr-interval "${value}"
done
