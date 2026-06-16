#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
#
# Authors:
# Tonghao Zhang <tonghao@bamaicloud.com>
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

# Verify the iotracing CLI end-to-end: a normal run emits well-formed
# JSON with the expected top-level schema and the dd workload we drive
# shows up under per-file attribution. Skipped on hosts without a
# writable ext4/xfs mount, since anyfsAttachOptions only hooks those.
# Path-validation rules are covered by internal/bpf TestValidateName.

set -exuo pipefail

source "${ROOT_DIR}/integration/lib.sh"

bpf_tool_setup iotracing
readonly DURATION=8

# Per-file attribution depends on anyfs probes binding to ext4/xfs
# (see cmd/iotracing/bpf_attach.go). Without such a mount the test can
# only validate the JSON envelope, which is too weak to be informative —
# skip rather than report a misleading PASS.
io_test_dir=""
while read -r mp; do
	[[ -d "${mp}" && -w "${mp}" ]] || continue
	io_test_dir=$(mktemp -d "${mp}/huatuo-iotracing.XXXXXX") && break
done < <(awk '$3 == "ext4" || $3 == "xfs" { print $2 }' /proc/mounts)

[[ -n "${io_test_dir}" ]] || skip "no writable ext4/xfs mount; iotracing accuracy needs anyfs probes to bind"

trap "rm -rf '${io_test_dir}'" EXIT

log_info "iotracing: duration=${DURATION}s, io_test_dir=${io_test_dir}"
"${TOOL_BIN}" --bpf-path "${TOOL_BPF}" \
	--duration "${DURATION}" \
	--output json \
	>"${TOOL_OUT}" 2>"${TOOL_ERR}" &
io_pid=$!

# Let probes attach before the workload starts.
sleep 1

# Drive sustained disk IO so rq_qos / io_schedule and the anyfs
# write_iter probes all see real requests. oflag=dsync syncs every
# block, keeping a single dd busy until timeout instead of restarting.
timeout "$((DURATION - 2))" dd if=/dev/zero of="${io_test_dir}/io" \
	bs=1M oflag=dsync status=none >/dev/null 2>&1 &

wait "${io_pid}" || dump_tool_logs_and_fail "iotracing exited non-zero"
[[ -s ${TOOL_OUT} ]] || dump_tool_logs_and_fail "iotracing produced empty output"

# Envelope: parses as JSON, both top-level keys exist as arrays.
jq -e '
	(.process_file_io_stats | type == "array") and
	(.io_schedule_timeout_stacks | type == "array")
' "${TOOL_OUT}" >/dev/null ||
	dump_tool_logs_and_fail "iotracing JSON schema invalid"

# Accuracy: a row attributed to dd holds a file under io_test_dir with non-zero
# write bps. comm may be "dd" (BPF capture) or "dd if=/dev/zero ..."
# (cmdline fallback when /proc still has the entry); match the leading
# token. One check exercises pid attribution, comm capture, path
# resolution, and bps accounting.
jq -e --arg dir "${io_test_dir}/" '
	.process_file_io_stats
	| map(select(.comm | test("^dd( |$)")))
	| map(.total_files[]?
		| select(.path | startswith($dir))
		| .fs_write_bps + .disk_write_bps)
	| any(. > 0)
' "${TOOL_OUT}" >/dev/null ||
	dump_tool_logs_and_fail "no dd-attributed write to ${io_test_dir} found"

log_info "PASS"
