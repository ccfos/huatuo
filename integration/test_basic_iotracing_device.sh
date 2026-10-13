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

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

require_commands go clang dd findmnt lsblk
[[ -r /sys/kernel/btf/vmlinux ]] || skip "kernel BTF is not readable"
kprobe_available blk_mq_start_request || skip "blk_mq_start_request is not available"

device="${HUATUO_IOTRACING_DEVICE:-$(findmnt -no SOURCE -T "${HUATUO_BAMAI_TEST_TMPDIR}")}"
# The block layer remaps partition bios to their containing disk.
if [[ $(lsblk -ndo TYPE "${device}") == part ]]; then
	device="/dev/$(lsblk -ndo PKNAME "${device}")"
fi
readonly DEVICE="${device}"
[[ -b "${DEVICE}" ]] || skip "set HUATUO_IOTRACING_DEVICE to a readable block device"
readonly OBJECT="${HUATUO_BAMAI_TEST_TMPDIR}/iotracing_device.bpf.o"
readonly TEST_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-device.log"

compile_bpf_fixture "${ROOT_DIR}/integration/testdata/iotracing_device.bpf.c" "${OBJECT}"

if ! HUATUO_IOTRACING_DEVICE_OBJECT="${OBJECT}" HUATUO_IOTRACING_DEVICE="${DEVICE}" \
	go test -mod=vendor -tags=integration \
	./integration/testdata/iotracing_device_linux_test.go -count=1 -v \
	> "${TEST_LOG}" 2>&1; then
	cat "${TEST_LOG}" >&2
	fatal "iotracing bio device integration test failed"
fi

cat "${TEST_LOG}"
