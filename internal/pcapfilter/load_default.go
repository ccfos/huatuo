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

//go:build !didi

package pcapfilter

import (
	"bytes"
	"fmt"

	"huatuo-bamai/internal/bpf"

	"github.com/cilium/ebpf"
)

// Load compiles filterExpr and injects it into obj, then loads the result
// through the default CollectionSpec-based path.
func Load(bpfName string, obj []byte, filterExpr string, consts map[string]any) (bpf.BPF, error) {
	spec, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(obj))
	if err != nil {
		return nil, fmt.Errorf("load collection spec: %w", err)
	}

	if err := Apply(spec, filterExpr); err != nil {
		return nil, fmt.Errorf("inject filters: %w", err)
	}

	return bpf.LoadBpfFromCollectionSpec(bpfName, spec, consts)
}
