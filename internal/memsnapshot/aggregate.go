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

package memsnapshot

// ObjectAggregate is the internal representation used by Java and Python
// readers before they are converted to the single public Entry shape.
type ObjectAggregate struct {
	TypeName     string
	Count        uint64
	ShallowBytes uint64
	AverageBytes float64
}

// SaturatingAdd keeps aggregate counters from wrapping when their sum
// exceeds the uint64 range.
func SaturatingAdd(left, right uint64) uint64 {
	if ^uint64(0)-left < right {
		return ^uint64(0)
	}
	return left + right
}

func EntriesFromObjects(objects []ObjectAggregate) []Entry {
	entries := make([]Entry, 0, len(objects))
	for index := range objects {
		object := &objects[index]
		entries = append(entries, Entry{
			Kind: "object_type", Name: object.TypeName, Bytes: object.ShallowBytes,
			Objects: object.Count, AverageBytes: object.AverageBytes,
		})
	}
	return entries
}
