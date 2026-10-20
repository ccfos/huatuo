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

package matcher

import (
	"fmt"
	"sync/atomic"
	"testing"
)

func TestCompileDoesNotRetainFailures(t *testing.T) {
	before := atomic.LoadInt64(&compiledCacheSize)
	if _, err := compile("[invalid"); err == nil {
		t.Fatal("expected compile error for invalid pattern")
	}
	if after := atomic.LoadInt64(&compiledCacheSize); after != before {
		t.Fatalf("failed compiles must not be cached: size %d -> %d", before, after)
	}
}

func TestCompileCacheBounded(t *testing.T) {
	atomic.StoreInt64(&compiledCacheSize, 0)
	defer atomic.StoreInt64(&compiledCacheSize, 0)

	// Compile more unique patterns than the cap; the generation reset
	// must keep the retained count within the bound.
	for i := 0; i < compiledCacheMax+64; i++ {
		pattern := fmt.Sprintf("k%d-([0-9])", i)
		if _, err := compile(pattern); err != nil {
			t.Fatal(err)
		}
	}
	if size := atomic.LoadInt64(&compiledCacheSize); size > compiledCacheMax {
		t.Fatalf("cache size %d exceeds cap %d", size, compiledCacheMax)
	}
}
