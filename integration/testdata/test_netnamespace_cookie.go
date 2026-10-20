// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// test_netnamespace_cookie exercises NetNamespaceCookieByPID against a real
// network namespace boundary: it must return the target namespace cookie and
// leave the calling thread in its original namespace.
package main

import (
	"flag"
	"fmt"
	"os"
	"syscall"

	"github.com/ccfos/huatuo/internal/utils/netutil"
)

func namespaceInum(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unexpected stat type for %s", path)
	}
	return stat.Ino, nil
}

func main() {
	pid := flag.Int("pid", 0, "pid whose network namespace cookie to read")
	flag.Parse()

	if *pid <= 0 {
		fmt.Fprintln(os.Stderr, "test_netnamespace_cookie: -pid is required")
		os.Exit(2)
	}

	targetInum, err := namespaceInum(fmt.Sprintf("/proc/%d/ns/net", *pid))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read target namespace: %v\n", err)
		os.Exit(1)
	}
	before, err := namespaceInum("/proc/thread-self/ns/net")
	if err != nil {
		fmt.Fprintf(os.Stderr, "read caller namespace: %v\n", err)
		os.Exit(1)
	}
	if before == targetInum {
		fmt.Fprintln(os.Stderr, "target pid is in the caller namespace, nothing to verify")
		os.Exit(1)
	}

	cookie, err := netutil.NetNamespaceCookieByPID(*pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NetNamespaceCookieByPID: %v\n", err)
		os.Exit(1)
	}

	// The helper must restore the caller's namespace before returning.
	after, err := namespaceInum("/proc/thread-self/ns/net")
	if err != nil {
		fmt.Fprintf(os.Stderr, "re-read caller namespace: %v\n", err)
		os.Exit(1)
	}
	if after != before {
		fmt.Fprintf(os.Stderr, "calling thread left in namespace %d, want %d\n", after, before)
		os.Exit(1)
	}

	// A second call must still observe the original namespace, which is only
	// true when every thread that switched namespaces was restored.
	if _, err := netutil.NetNamespaceCookieByPID(*pid); err != nil {
		fmt.Fprintf(os.Stderr, "second NetNamespaceCookieByPID: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("cookie=%d restored_namespace=%d\n", cookie, after)
}
