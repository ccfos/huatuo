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
require_commands go python3 objcopy
require_readable /proc/self/status
btf_cap_eff=$(awk '$1 == "CapEff:" { print $2 }' /proc/self/status)
(((16#${btf_cap_eff} & (1 << 18)) != 0)) \
	|| skip "isolated BTF source checks require CAP_SYS_CHROOT"

btf_probe="${HUATUO_BAMAI_TEST_TMPDIR}/kernel-btf-check"
CGO_ENABLED=0 go -C "${ROOT_DIR}" build -mod=vendor -o "${btf_probe}" \
	"${ROOT_DIR}/integration/testdata/test_kernel_btf.go" \
	|| fatal "cannot build kernel BTF probe"

assert_kernel_btf_source() {
	local mode=$1 expected=$2 status=0
	local root="${HUATUO_BAMAI_TEST_TMPDIR}/btf-${mode}"
	mkdir -p "${root}"
	cp "${btf_probe}" "${root}/probe"
	python3 - "${root}" "${mode}" << 'PY'
import pathlib
import platform
import struct
import subprocess
import sys

root = pathlib.Path(sys.argv[1])
mode = sys.argv[2]
# One integer type suffices to exercise raw and ELF BTF parsing independently
# of the running host's BTF configuration.
types = struct.pack("=IIII", 1, 1 << 24, 4, (1 << 24) | 32)
strings = b"\0int\0"
raw = struct.pack("=HBBIIIII", 0xEB9F, 1, 0, 24, 0, len(types), len(types), len(strings)) + types + strings
if mode in ("sysfs", "corrupt-sysfs"):
    target = root / "sys/kernel/btf/vmlinux"
    target.parent.mkdir(parents=True)
    target.write_bytes(raw if mode == "sysfs" else b"invalid BTF")
elif mode in ("elf", "corrupt-elf"):
    btf = root / "fixture.btf"
    btf.write_bytes(raw if mode == "elf" else b"invalid BTF")
    target = root / "boot" / ("vmlinux-" + platform.release())
    target.parent.mkdir(parents=True)
    subprocess.run(["objcopy", "--add-section", ".BTF=" + str(btf), str(root / "probe"), str(target)], check=True)
PY
	python3 - "${root}" << 'PY' > "${root}/probe.log" 2>&1 || status=$?
import os
import sys

os.chroot(sys.argv[1])
os.chdir("/")
os.execv("/probe", ["/probe"])
PY
	assert_eq "${status}" "${expected}" "${mode}: kernel BTF probe exit status" \
		|| fatal "unexpected BTF probe result (see ${root}/probe.log)"
	if [[ ${mode} == missing ]]; then
		grep -qF 'no BTF found for kernel version' "${root}/probe.log" \
			|| fatal "missing BTF diagnostic must identify the running kernel (see ${root}/probe.log)"
	elif [[ ${mode} == corrupt-* ]]; then
		[[ -s "${root}/probe.log" ]] \
			|| fatal "corrupt BTF must produce a diagnostic (see ${root}/probe.log)"
	fi
}

assert_kernel_btf_source missing 77
assert_kernel_btf_source sysfs 0
assert_kernel_btf_source elf 0
assert_kernel_btf_source corrupt-sysfs 1
assert_kernel_btf_source corrupt-elf 1
log_info "kernel BTF probe distinguishes missing, valid and corrupt sysfs/vmlinux ELF sources"
