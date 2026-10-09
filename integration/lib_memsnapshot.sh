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

# Shared memory snapshot workloads for integration and E2E tests.
set -euo pipefail

# Shared runtime workloads use the a/p/q protocol in integration and E2E.
snapshot_build_runtime_fixtures() {
	if [[ ${MEMSNAP_OPTIONAL_RUNTIMES:-0} != 1 ]]; then
		require_commands python3 java javac gcc g++
	fi
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/snapshot.py" << 'PYTHON'
import gc
import sys
class MemsnapshotPayload:
    __slots__ = ("payload",)
objects = []
pressure = None
while True:
    command = sys.stdin.read(1)
    if command == "a":
        objects = [MemsnapshotPayload() for _ in range(20000)]
        for obj in objects:
            obj.payload = list(range(64))
        gc.collect()
        print("object_bytes=" + str(sys.getsizeof(objects[0])), flush=True)
        print("ready", flush=True)
    elif command == "p":
        pressure = bytearray(80 * 1024 * 1024)
        print("pressure ready", flush=True)
    elif command == "q" or not command:
        break
PYTHON
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/HeapFixture.java" << 'JAVA'
public class HeapFixture {
 static Object[] retained;
 static Payload[][] large = new Payload[6][];
 static byte[] pressure;
 static class Payload { long a,b,c,d,e,f,g,h; }
 public static void main(String[] args) throws Exception {
  int command;
  while ((command=System.in.read())!=-1) {
   if(command=='a') {
    retained=new Object[200000];
    for(int i=0;i<retained.length;i++) retained[i]=new Payload();
    for(int i=0;i<large.length;i++) large[i]=new Payload[300000];
    System.gc(); System.out.println("ready");
   } else if(command=='p') {
    pressure=new byte[64*1024*1024];
    for(int i=0;i<pressure.length;i+=4096) pressure[i]=1;
    System.out.println("pressure ready");
   } else if(command=='q') break;
  }
 }
}
JAVA
	if command -v java > /dev/null && command -v javac > /dev/null; then
		javac -source 8 -target 8 "${HUATUO_BAMAI_TEST_TMPDIR}/HeapFixture.java"
	fi
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/snapshot.c" << 'NATIVE'
#include <stdio.h>
#include <stdlib.h>
int main(void) {
 unsigned char *retained=NULL, *pressure=NULL;
 int command;
 while((command=getchar())!=EOF) {
  if(command=='a' || command=='p') {
   size_t size=(command=='a'?8:80)*1024*1024;
   unsigned char *p=(unsigned char *)malloc(size);
   if(!p) return 1;
   for(size_t i=0;i<size;i+=4096) p[i]=1;
   if(command=='a') retained=p; else pressure=p;
   puts(command=='a'?"ready":"pressure ready"); fflush(stdout);
  } else if(command=='q') break;
 }
 free(retained); free(pressure); return 0;
}
NATIVE
	if command -v gcc > /dev/null; then
		gcc -O0 -o "${HUATUO_BAMAI_TEST_TMPDIR}/c-snapshot" "${HUATUO_BAMAI_TEST_TMPDIR}/snapshot.c"
	fi
	# The iostream reference keeps libstdc++ mapped for runtime classification.
	{
		echo '#include <iostream>'
		sed 's/puts(command==/std::cout << ""; puts(command==/' "${HUATUO_BAMAI_TEST_TMPDIR}/snapshot.c"
	} > "${HUATUO_BAMAI_TEST_TMPDIR}/snapshot.cc"
	if command -v g++ > /dev/null; then
		g++ -O0 -o "${HUATUO_BAMAI_TEST_TMPDIR}/cpp-snapshot" "${HUATUO_BAMAI_TEST_TMPDIR}/snapshot.cc"
	fi
}

# These long-running tracers increment hitcount when Start returns, not per event.
# A zero count after a trigger also detects an unexpected tracer restart.
memsnapshot_assert_tracing_metrics() {
	local tracer=$1 region=$2 enabled=$3
	huatuo_bamai_collect_metrics
	awk -v tracer="${tracer}" -v region="${region}" -v enabled="${enabled}" '
 function labels_ok(line) {
  return line ~ /[{,]host="[^"]+"/ && index(line, "region=\"" region "\"") > 0
 }
 $0 == "# TYPE huatuo_bamai_tracing_status_hitcount gauge" { hit_type=1 }
 $0 == "# TYPE huatuo_bamai_tracing_status_running gauge" { running_type=1 }
 index($0,"huatuo_bamai_tracing_status_hitcount{")==1 && $0 ~ ("[{,]tracing=\"" tracer "\"") {
  hits++
  if (!labels_ok($0) || $NF !~ /^[0-9]+$/ || $NF+0 != 0) bad=1
 }
 index($0,"huatuo_bamai_tracing_status_running{")==1 {
  running++
  if (!labels_ok($0) || $NF !~ /^[0-9]+$/ || $NF+0 != enabled) bad=1
 }
 END { exit bad || hits != enabled || running != 1 || !running_type || (enabled && !hit_type) }
 ' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" || {
		cat "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" >&2
		fatal "invalid tracing metrics for ${tracer}: expected enabled=${enabled}, running=${enabled}, hitcount=0"
	}
}

# Build the shared test-only remote-read barrier source.
snapshot_build_read_barrier() {
	read_source="${MEMSNAP_ACCEPTANCE_ARTIFACTS}/read-barrier.go"
	awk '
 /^import \(/ { print; print "\"strconv\""; next }
 /^func ProcessVMReadv\(/ { sub("ProcessVMReadv", "integrationProcessVMReadv") }
 { print }
' "${ROOT_DIR}/vendor/golang.org/x/sys/unix/zsyscall_linux.go" > "${read_source}"
	cat >> "${read_source}" << 'GO'

func integrationReadMark(path string) error {
 fd, err := Open(path, O_WRONLY|O_CREAT|O_EXCL|O_CLOEXEC, 0600)
 if err != nil { return err }
 return Close(fd)
}

func ProcessVMReadv(pid int, local []Iovec, remote []RemoteIovec, flags uint) (int, error) {
 dir, _ := syscall.Getenv("MEMSNAP_READ_BARRIER_DIR")
 target, _ := syscall.Getenv("MEMSNAP_READ_BARRIER_PID")
 active := dir != "" && target == strconv.Itoa(pid)
 if active && Access(dir+"/first-read", F_OK) == nil {
  if err := integrationReadMark(dir+"/reached"); err == nil {
   released := false
   for i:=0; i<1000; i++ {
    if Access(dir+"/release", F_OK) == nil { released=true; break }
    pause:=Timespec{Nsec:10000000}
    _ = Nanosleep(&pause,nil)
   }
   if !released { return 0, ETIMEDOUT }
  }
 }
 n, err := integrationProcessVMReadv(pid,local,remote,flags)
 if active {
  if n>0 && err==nil { _ = integrationReadMark(dir+"/first-read") }
  if err==ESRCH { _ = integrationReadMark(dir+"/target-exited") }
 }
 return n,err
}
GO
}
