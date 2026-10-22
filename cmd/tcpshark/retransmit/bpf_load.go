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

package retransmit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	pcap "github.com/ccfos/huatuo/internal/pcapfilter"
)

const perfEventMapName = "perf_events"

func loadBPF(
	bpfPath string,
	filterExpr string,
	bpfLimiter *bpf.RateLimiter,
) (bpf.BPF, error) {
	bpfBytes, err := os.ReadFile(bpfPath)
	if err != nil {
		return nil, fmt.Errorf("read bpf object %q: %w", bpfPath, err)
	}

	baseName := filepath.Base(bpfPath)
	objectName := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	return pcap.Load(
		fmt.Sprintf("%s_%d.o", objectName, time.Now().UnixNano()),
		bpfBytes,
		filterExpr,
		bpfLimiter.Constants(nil),
	)
}

func (t *Tracer) attachBPF(isTLPEnabled bool) error {
	if t.limiter.Enabled() {
		if err := t.limiter.OpenEventPipe(t.ctx, t.bpf); err != nil {
			return err
		}
	}
	reader, err := t.bpf.EventPipeByName(t.ctx, perfEventMapName, bpf.DefaultPerfEventBufferBytes)
	if err != nil {
		return fmt.Errorf("open event pipe: %w", err)
	}
	t.reader = reader
	if err := t.ctx.Err(); err != nil {
		return err
	}
	if err := t.bpf.AttachWithOptions(attachOptions(isTLPEnabled)); err != nil {
		return fmt.Errorf("attach programs: %w", err)
	}
	return nil
}

func attachOptions(isTLPEnabled bool) []bpf.AttachOption {
	options := []bpf.AttachOption{
		{
			ProgramName: "retrans_skb",
			Symbol:      "tcp/tcp_retransmit_skb",
		},
		{
			ProgramName: "retrans_synack",
			Symbol:      "tcp/tcp_retransmit_synack",
		},
	}
	if isTLPEnabled {
		options = append(options, bpf.AttachOption{
			ProgramName: "retrans_tlp",
			Symbol:      "tcp_send_loss_probe",
		})
	}

	return options
}
