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

//go:build didi

package events

import (
	"context"
	"errors"

	"github.com/ccfos/huatuo/internal/bpf"
)

// startNetRxLatencyBPF loads the kprobe-only object and attaches it.
//
// The Didi backend implements the loader itself and has no tracing variant
// selection, and this build must not require the object that carries an fentry
// program. It therefore keeps the original kprobe path unchanged.
func startNetRxLatencyBPF(
	ctx context.Context,
	bpfName string,
	consts map[string]any,
) (bpf.BPF, bpf.PerfEventReader, error) {
	object, err := bpf.LoadBPF(bpfName, consts)
	if err != nil {
		return nil, nil, err
	}

	reader, err := object.AttachAndEventPipe(ctx, netRxLatencyEventMap, bpf.DefaultPerfEventBufferBytes)
	if err != nil {
		return nil, nil, errors.Join(err, object.Close())
	}

	return object, reader, nil
}
