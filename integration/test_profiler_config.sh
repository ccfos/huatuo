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

readonly CONFIG_PROFILER="${ROOT_DIR}/_output/bin/profiler"
readonly CONFIG_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/profiler-config.log"

assert_profiler_config_rejected() {
	local expected=$1
	shift
	local status=0
	timeout --kill-after=1 5 "${CONFIG_PROFILER}" --type cpu --language c --pid "$$" \
		--output-format collapsed --output-path "${HUATUO_BAMAI_TEST_TMPDIR}/profile" --log-level info \
		"$@" > "${CONFIG_LOG}" 2>&1 || status=$?
	[[ ${status} -eq 1 ]] || fatal "expected configuration exit 1, got ${status}: $*"
	grep -qF -- "${expected}" "${CONFIG_LOG}" \
		|| fatal "expected '${expected}', got: $(cat "${CONFIG_LOG}")"
	if grep -qE 'init bpf|using profiler:|data reading loop started' "${CONFIG_LOG}"; then
		fatal "invalid configuration reached collection: $(cat "${CONFIG_LOG}")"
	fi
	[[ ! -e "${HUATUO_BAMAI_TEST_TMPDIR}/profile" ]] || fatal "invalid configuration created profile output"
}

require_commands timeout

assert_profiler_config_rejected '--duration must be between 1 and 9223372036 seconds' --duration 9223372037
assert_profiler_config_rejected 'exceeds duration' \
	--duration 9223372036 --aggr-interval 9223372037
for value in 0 -1; do
	assert_profiler_config_rejected 'frequency must be at least 1' --freq "${value}"
	assert_profiler_config_rejected 'duration must be at least 1' --duration "${value}"
	assert_profiler_config_rejected 'aggregation interval must be at least 1' --aggr-interval "${value}"
done
assert_profiler_config_rejected 'exceeds duration' --duration 1 --aggr-interval 2
assert_profiler_config_rejected 'Incorrect Usage' --freq invalid
assert_profiler_config_rejected 'invalid cpuid' --cpuid invalid
assert_profiler_config_rejected 'invalid cpuid range' --cpuid 2-1
assert_profiler_config_rejected 'out of range' --cpuid 2147483647
assert_profiler_config_rejected 'invalid cpuid' --cpuid -1
assert_profiler_config_rejected 'invalid --pid' --pid '1,1'

assert_profiler_config_rejected '--cpu-mode is valid only when --type=cpu' \
	--type memory --memory-mode virtual_alloc --cpu-mode oncpu
assert_profiler_config_rejected 'unsupported CPU mode' --cpu-mode invalid
assert_profiler_config_rejected '--memory-mode is only valid when --type=memory' --memory-mode virtual_alloc
assert_profiler_config_rejected '--memory-mode is required' --type memory
assert_profiler_config_rejected 'is not supported for c' --type memory --memory-mode object_alloc
assert_profiler_config_rejected 'is not supported for java' --language java --type memory --memory-mode physical_alloc
assert_profiler_config_rejected '--cpu-mode=offcpu is supported only by native CPU profiling' \
	--language java --cpu-mode offcpu
assert_profiler_config_rejected '--freq is not used with --cpu-mode=offcpu' --cpu-mode offcpu --freq 99
assert_profiler_config_rejected 'Java profiler frequency must not exceed 1000' --language java --freq 1001

# These errors must precede tool discovery, process attachment, and BPF setup.
for language in c go c++ java python; do
	assert_profiler_config_rejected '--pid and --container-id are mutually exclusive' \
		--language "${language}" --container-id aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
done
for interval in 1 3; do
	assert_profiler_config_rejected 'must equal --duration' \
		--language python --duration 2 --aggr-interval "${interval}"
done
for value in 0 -1; do
	assert_profiler_config_rejected 'duration must be at least 1' \
		--language python --tool-path "${HUATUO_BAMAI_TEST_TMPDIR}" --duration "${value}" --aggr-interval "${value}"
done
