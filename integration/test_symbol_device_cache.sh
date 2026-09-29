#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors
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

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

for tool in clang mke2fs mount umount; do
	command -v "$tool" > /dev/null || fatal "symbol cache integration prerequisite $tool is not installed"
done

fixture_dir=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/symbol-device-cache.XXXXXX")
mounted=()
cleanup() {
	local status=$?
	local unmount_failed=0
	trap - EXIT
	for ((i = ${#mounted[@]} - 1; i >= 0; i--)); do
		if ! umount "${mounted[i]}"; then
			log_error "failed to unmount symbol cache fixture: ${mounted[i]}"
			unmount_failed=1
		fi
	done
	if ((unmount_failed == 0)); then
		rm -rf -- "$fixture_dir" || status=1
	else
		status=1
	fi
	exit "$status"
}
trap cleanup EXIT

mkdir -p "$fixture_dir/stage0" "$fixture_dir/stage1" "$fixture_dir/mount0" "$fixture_dir/mount1"
cat > "$fixture_dir/program.c" << 'SOURCE_C'
#include <dlfcn.h>
#include <stdio.h>
void ENTRY(void) {}
int main(int argc, char **argv) {
 if (argc != 3) return 1;
 void *handle = dlopen(argv[1], RTLD_NOW);
 if (!handle) return 2;
 void *function = dlsym(handle, argv[2]);
 if (!function) return 3;
 printf("%p %p\n", (void *)ENTRY, function);
 fflush(stdout);
 getchar();
 dlclose(handle);
 return 0;
}
SOURCE_C
printf 'void ENTRY(void) {}\n' > "$fixture_dir/library.c"

for i in 0 1; do
	clang -O0 -g -fno-pie -no-pie "-DENTRY=image_${i}_entry" \
		-o "$fixture_dir/stage$i/program" "$fixture_dir/program.c" -ldl
	clang -shared -fPIC "-DENTRY=image_${i}_library" \
		-o "$fixture_dir/stage$i/library.so" "$fixture_dir/library.c"
done

# Cloned ext2 images keep inode numbers equal across distinct loop devices.
truncate -s 16M "$fixture_dir/first.img"
mke2fs -q -F -t ext2 -m 0 -d "$fixture_dir/stage0" "$fixture_dir/first.img"
cp "$fixture_dir/first.img" "$fixture_dir/second.img"
mount -t ext4 -o loop "$fixture_dir/first.img" "$fixture_dir/mount0"
mounted+=("$fixture_dir/mount0")
mount -t ext4 -o loop "$fixture_dir/second.img" "$fixture_dir/mount1"
mounted+=("$fixture_dir/mount1")

# Redirection truncates the cloned files without replacing their inodes.
for name in program library.so; do
	cat "$fixture_dir/stage1/$name" > "$fixture_dir/mount1/$name"
	ln "$fixture_dir/mount0/$name" "$fixture_dir/mount0/$name-alias"
done

export HUATUO_SYMBOL_DEVICE_CACHE_MOUNT0="$fixture_dir/mount0"
export HUATUO_SYMBOL_DEVICE_CACHE_MOUNT1="$fixture_dir/mount1"
log_info "validating symbol cache identity across filesystem devices"
(
	cd "${ROOT_DIR}"
	go test -mod=vendor -tags=integration -count=1 -v \
		-run '^TestSymbolDeviceCache$' \
		./integration/testdata/test_symbol_device_cache_linux_test.go
)
