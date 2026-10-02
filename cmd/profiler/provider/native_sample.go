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

package provider

import "slices"

type processKey struct {
	PID  uint32
	Comm string
}

type rawStackIDs struct {
	KernelStackID int32
	UserStackID   int32
}

// BPF may reuse a user stack ID across process address spaces.
type userStackCacheKey struct {
	PID     uint32
	StackID int32
}

// symbolizedStackTrace stores raw outermost-first frames. The frame slices are
// immutable after the sample is enqueued so cached symbolization can be shared.
type symbolizedStackTrace struct {
	UserFrames   []string
	KernelFrames []string
}

func (s symbolizedStackTrace) frameCount() int {
	return len(s.UserFrames) + len(s.KernelFrames)
}

func (s symbolizedStackTrace) appendTo(frames []string) []string {
	frames = slices.Grow(frames, s.frameCount())
	frames = append(frames, s.UserFrames...)
	return append(frames, s.KernelFrames...)
}

type stackSample struct {
	Process    processKey
	StackTrace symbolizedStackTrace
	Value      int64
	Category   string
}

type lockSample struct {
	Process         processKey
	LockAddress     uint64
	StackTrace      symbolizedStackTrace
	WaitNanoseconds uint64
	ContentionCount uint32
}
