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
	if "${CONFIG_PROFILER}" --type cpu --language c --pid "$$" \
		--output-format collapsed --output-path "${HUATUO_BAMAI_TEST_TMPDIR}/profile" \
		"$@" > "${CONFIG_LOG}" 2>&1; then
		fatal "expected configuration failure for $*"
	fi
	grep -qF -- "${expected}" "${CONFIG_LOG}" \
		|| fatal "expected '${expected}', got: $(cat "${CONFIG_LOG}")"
	if grep -qF 'init bpf' "${CONFIG_LOG}"; then
		fatal "configuration reached BPF initialization: $(cat "${CONFIG_LOG}")"
	fi
}

assert_profiler_config_rejected '--duration must be between 1 and 9223372036 seconds' --duration 9223372037
assert_profiler_config_rejected 'invalid cpuid' --cpuid invalid
assert_profiler_config_rejected 'invalid --pid' --pid '1,1'
