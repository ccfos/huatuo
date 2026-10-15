#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
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

# Validate resource capture independently of BPF and feature resource budgets.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_resources.sh"

readonly RESOURCES_SUCCESS_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/resources-success"
readonly RESOURCES_ABORT_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/resources-abort"
readonly RESOURCES_EXIT_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/resources-exit"
readonly RESOURCES_KILL_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/resources-kill"
readonly RESOURCES_INVALID_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/resources-invalid"
readonly RESOURCES_REPLACED_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/resources-replaced"
readonly RESOURCES_STOPPED_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/resources-stopped"

require_commands go jq realpath sleep getconf ps yes
resources_target_pid=""
resources_active_dir=""
cleanup() {
	[[ -z ${resources_active_dir} ]] || resource_cleanup "${resources_active_dir}"
	if [[ -n ${resources_target_pid} ]]; then
		kill "${resources_target_pid}" 2> /dev/null || true
		wait "${resources_target_pid}" 2> /dev/null || true
	fi
}
trap cleanup EXIT

# testdata packages are not discovered by go test ./...; exercise them here.
(cd "${ROOT_DIR}" && go test -mod=vendor -tags integration ./integration/testdata/resource_sampler) \
	|| fatal "resource sampler calculation or live-counter tests failed"
resource_build || fatal "failed to build resource sampler"
sleep 60 &
resources_target_pid=$!

resources_active_dir=${RESOURCES_SUCCESS_DIR}
resource_start "${resources_target_pid}" "${resources_active_dir}" || fatal "resource sampler did not become ready"
# A defined observation duration, not a guess about service readiness.
sleep 0.5
resource_stop "${resources_active_dir}" || fatal "normal resource capture failed"
jq -e --argjson pid "${resources_target_pid}" '
  .format_version == 2 and .complete == true and .pid == $pid
  and .quality.usable_for_peak == true and .quality.reasons == []
  and .errors == [] and .periodic_sample_count >= 1
  and .cpu_window_seconds > 0 and .io_window_seconds > 0
  and .cpu_seconds >= 0 and .cpu_percent.mean >= 0
  and .fds.before > 0 and .fds.after > 0 and .fds.peak >= .fds.after
  and .rss_bytes.peak >= .rss_bytes.after
  and .io.read_bytes.delta >= 0 and .io.wchar.per_second >= 0
' "${resources_active_dir}/result.json" > /dev/null || fatal "resource result contract failed"
log_info "resource sample: $(jq -c . "${resources_active_dir}/result.json")"
resource_cleanup "${resources_active_dir}"
resource_cleanup "${resources_active_dir}"
kill -0 "${resources_target_pid}" || fatal "sampler stopped the target"
if resource_start "${resources_target_pid}" "${RESOURCES_SUCCESS_DIR}"; then
	fatal "existing capture directory was overwritten"
fi

if resource_start 0 "${RESOURCES_INVALID_DIR}"; then
	fatal "invalid target PID was accepted"
fi
kill -0 "${resources_target_pid}" || fatal "invalid capture stopped the target"

resources_active_dir=${RESOURCES_ABORT_DIR}
resource_start "${resources_target_pid}" "${resources_active_dir}" || fatal "abort capture did not start"
if (resource_stop "${resources_active_dir}"); then
	fatal "subshell stopped its parent's capture"
fi
resource_cleanup "${RESOURCES_SUCCESS_DIR}"
kill -0 "$(jq -r '.sampler_pid' "${resources_active_dir}/ready.json")" || fatal "wrong-directory cleanup stopped an active sampler"
resource_cleanup "${resources_active_dir}"
jq -e '.complete == false and (.errors | length) > 0' "${resources_active_dir}/result.json" > /dev/null \
	|| fatal "aborted capture was reported as complete"
kill -0 "${resources_target_pid}" || fatal "cleanup stopped the target"

resources_active_dir=${RESOURCES_REPLACED_DIR}
resource_start "${resources_target_pid}" "${resources_active_dir}" || fatal "replacement capture did not start"
resources_sampler_pid=$(jq -r '.sampler_pid' "${resources_active_dir}/ready.json")
mv -- "${resources_active_dir}" "${resources_active_dir}-saved"
ln -s -- "${RESOURCES_SUCCESS_DIR}" "${resources_active_dir}"
resource_cleanup "${resources_active_dir}"
if kill -0 "${resources_sampler_pid}" 2> /dev/null; then
	fatal "directory replacement prevented sampler cleanup"
fi
jq -e '.complete == false' "${resources_active_dir}-saved/result.json" > /dev/null \
	|| fatal "sampler did not retain the original output directory"
kill -0 "${resources_target_pid}" || fatal "replacement cleanup stopped the target"

resources_active_dir=${RESOURCES_STOPPED_DIR}
resource_start "${resources_target_pid}" "${resources_active_dir}" || fatal "stopped capture did not start"
resources_sampler_pid=$(jq -r '.sampler_pid' "${resources_active_dir}/ready.json")
kill -STOP "${resources_sampler_pid}"
resource_cleanup "${resources_active_dir}"
if kill -0 "${resources_sampler_pid}" 2> /dev/null; then
	fatal "stopped sampler was not reaped"
fi
kill -0 "${resources_target_pid}" || fatal "stopped sampler cleanup killed the target"

resources_active_dir=${RESOURCES_KILL_DIR}
resource_start "${resources_target_pid}" "${resources_active_dir}" || fatal "kill capture did not start"
resources_sampler_pid=$(jq -r '.sampler_pid' "${resources_active_dir}/ready.json")
kill -KILL "${resources_sampler_pid}" || fatal "could not kill owned sampler"
if resource_stop "${resources_active_dir}"; then
	fatal "killed sampler was reported as successful"
fi
kill -0 "${resources_target_pid}" || fatal "sampler failure stopped the target"

resources_active_dir=${RESOURCES_EXIT_DIR}
resource_start "${resources_target_pid}" "${resources_active_dir}" || fatal "exit capture did not start"
kill "${resources_target_pid}"
wait "${resources_target_pid}" || true
resources_target_pid=""
if resource_stop "${resources_active_dir}"; then
	fatal "exited target was reported as a successful capture"
fi
jq -e '.complete == false and (.errors | length) > 0' "${resources_active_dir}/result.json" > /dev/null \
	|| fatal "target exit diagnostics are missing"
log_info "resource lifecycle test passed"
