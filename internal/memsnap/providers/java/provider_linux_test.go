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

package java

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/memsnap"
)

func TestJavaDiscoveryAndFailureClassification(t *testing.T) {
	root := t.TempDir()
	if _, err := discoverVM(t.Context(), root, 1); err == nil || errors.Is(err, errHotSpotUnavailable) {
		t.Fatalf("missing maps must be an inspection failure: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "1/maps"), []byte("1000-2000 r-xp 00000000 00:00 0 /bin/native\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverVM(t.Context(), root, 1); !errors.Is(err, errHotSpotUnavailable) {
		t.Fatalf("non-HotSpot mapping must be unsupported: %v", err)
	}
	identity, err := memsnap.ReadIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	request := memsnap.Request{Identity: identity, TopK: 10}
	snapshot, err := NewProvider().Capture(t.Context(), request)
	if err != nil || snapshot.Status != memsnap.StatusUnavailable {
		t.Fatalf("non-JVM result: %+v, %v", snapshot, err)
	}
	request.Identity.StartTimeTicks++
	snapshot, err = NewProvider().Capture(t.Context(), request)
	if err != nil || snapshot.Status != memsnap.StatusFailed {
		t.Fatalf("stale identity result: %+v, %v", snapshot, err)
	}
}

func TestJavaMetadataAndSamplingBounds(t *testing.T) {
	metadata := &vmMeta{}
	if err := metadata.reserveMetadata(maxCachedMetadataBytes); err != nil {
		t.Fatal(err)
	}
	if err := metadata.reserveMetadata(1); !errors.Is(err, errHotSpotUnavailable) || metadata.retainedBytes != maxCachedMetadataBytes {
		t.Fatalf("metadata budget exceeded: %v", err)
	}
	version, err := parseJavaRelease(t.Context(), strings.NewReader("JAVA_VERSION=\"17.0.12\"\n"))
	if err != nil || version != "17.0.12" {
		t.Fatalf("release: %q, %v", version, err)
	}
	if _, err := parseJavaRelease(t.Context(), strings.NewReader(strings.Repeat("X=1\n", 257))); err == nil {
		t.Fatal("unbounded release metadata")
	}
	regions := []region{{bottom: 4096, top: 4096 + 1<<20}}
	windows := planWindows(regions, 7, 8192, 4096)
	var sampled uint64
	for _, w := range windows {
		if w.start < regions[0].bottom || w.start+w.size > regions[0].top {
			t.Fatal("sample outside region")
		}
		sampled += w.size
	}
	if sampled == 0 || sampled > 8192 {
		t.Fatalf("sample budget = %d", sampled)
	}
	snapshot := &memsnap.Snapshot{}
	finishStatus(snapshot, 0, 1<<20, sampled)
	if snapshot.Status != memsnap.StatusPartial || !strings.Contains(snapshot.Reason, "bounded") {
		t.Fatalf("bounded sample not marked partial: %+v", snapshot)
	}
}

func TestJavaRegionAndObjectDecoding(t *testing.T) {
	metadata := &vmMeta{constants: map[string]int64{
		"HeapRegionType::StartsHumongousTag":    12,
		"HeapRegionType::ContinuesHumongousTag": 13,
		"Klass::_lh_header_size_shift":          16,
		"Klass::_lh_header_size_mask":           255,
		"Klass::_lh_log2_element_size_mask":     255,
	}}
	regions := []region{
		{bottom: 4096, top: 8192, capacity: 4096, tag: 12, hasTag: true},
		{bottom: 8192, top: 9000, capacity: 4096, tag: 13, hasTag: true},
	}
	heap, err := groupRegions(regions, metadata)
	if err != nil || len(heap.humongous) != 1 || len(heap.humongous[0].regions) != 2 {
		t.Fatalf("humongous grouping: %+v, %v", heap, err)
	}
	if _, err := groupRegions(regions[1:], metadata); !errors.Is(err, errHotSpotUnavailable) {
		t.Fatalf("orphan continuation accepted: %v", err)
	}
	raw := make([]byte, 24)
	binary.LittleEndian.PutUint32(raw[12:], 3)
	layout := uint32(0x80000000 | 16<<16 | 2)
	size, err := objectSize(raw, &klass{layoutHelper: int32(layout)}, metadata, 0, 12)
	if err != nil || size != 32 {
		t.Fatalf("array size: %d, %v", size, err)
	}
	if _, err := objectSize(raw[:12], &klass{layoutHelper: int32(layout)}, metadata, 0, 12); err == nil {
		t.Fatal("truncated array header accepted")
	}
}
