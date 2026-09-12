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

package procfs

import "github.com/prometheus/procfs"

type Proc = procfs.Proc

// NetDevLine is one line of /proc/[pid]/net/dev for a single interface.
type NetDevLine = procfs.NetDevLine

func Self() (procfs.Proc, error) {
	fs, err := NewFS(defaultProcMountPoint)
	if err != nil {
		return procfs.Proc{}, err
	}
	return fs.Self()
}

// NewProc returns a process for the given pid under /proc.
func NewProc(pid int) (procfs.Proc, error) {
	fs, err := NewFS(defaultProcMountPoint)
	if err != nil {
		return procfs.Proc{}, err
	}
	return fs.Proc(pid)
}
