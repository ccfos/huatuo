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

import (
	"encoding/binary"
	"hash/maphash"
	"slices"
)

type stackTraceID uint32

type internedStackTrace struct {
	frames []string
	next   stackTraceID
}

type stackTraceInterner struct {
	seed   maphash.Seed
	heads  map[uint64]stackTraceID
	traces []internedStackTrace
}

func (i *stackTraceInterner) LookupOrAdd(frames []string) stackTraceID {
	if len(frames) == 0 {
		return 0
	}

	i.initialize()
	return i.lookupOrAddHashed(hashStackTrace(i.seed, frames), frames)
}

func (i *stackTraceInterner) Frames(id stackTraceID) []string {
	if id == 0 {
		return nil
	}
	return i.traces[id-1].frames
}

func (i *stackTraceInterner) Reset() {
	*i = stackTraceInterner{}
}

func (i *stackTraceInterner) initialize() {
	if i.heads != nil {
		return
	}

	i.seed = maphash.MakeSeed()
	i.heads = make(map[uint64]stackTraceID)
}

func (i *stackTraceInterner) lookupOrAddHashed(stackHash uint64, frames []string) stackTraceID {
	for id := i.heads[stackHash]; id != 0; {
		trace := &i.traces[id-1]
		if slices.Equal(trace.frames, frames) {
			return id
		}
		id = trace.next
	}

	// symbolizedStackTrace remains immutable after enqueue, so retaining the
	// slice avoids another full-stack copy on this hot path.
	id := stackTraceID(len(i.traces) + 1)
	i.traces = append(i.traces, internedStackTrace{
		frames: frames,
		next:   i.heads[stackHash],
	})
	i.heads[stackHash] = id
	return id
}

func hashStackTrace(seed maphash.Seed, frames []string) uint64 {
	var hash maphash.Hash
	hash.SetSeed(seed)
	writeStackTraceHashUint(&hash, uint64(len(frames)))
	for _, frame := range frames {
		writeStackTraceHashUint(&hash, uint64(len(frame)))
		_, _ = hash.WriteString(frame)
	}
	return hash.Sum64()
}

func writeStackTraceHashUint(hash *maphash.Hash, value uint64) {
	var encoded [8]byte
	binary.LittleEndian.PutUint64(encoded[:], value)
	_, _ = hash.Write(encoded[:])
}
