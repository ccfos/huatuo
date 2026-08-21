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

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/pcapfilter"
)

func loadRetransmitBPF(
	bpfPath string,
	filterExpr string,
	bpfLimiter *bpf.RateLimiter,
) (bpf.BPF, error) {
	bpfBytes, err := os.ReadFile(bpfPath)
	if err != nil {
		return nil, fmt.Errorf("read bpf: %w", err)
	}

	bpfName := fmt.Sprintf("tcp_retransmit_%d.o", time.Now().UnixNano())
	return pcapfilter.Load(
		bpfName,
		bpfBytes,
		filterExpr,
		bpfLimiter.Constants(nil),
	)
}

func attachRetransmitPrograms(
	ctx context.Context,
	bpfObj bpf.BPF,
	isTLPEnabled bool,
) (bpf.PerfEventReader, error) {
	reader, err := bpfObj.EventPipeByName(ctx, "perf_events", 8192)
	if err != nil {
		return nil, fmt.Errorf("open event pipe: %w", err)
	}

	if err := bpfObj.AttachWithOptions(retransmitAttachOptions(isTLPEnabled)); err != nil {
		attachErr := fmt.Errorf("attach programs: %w", err)
		if closeErr := reader.Close(); closeErr != nil {
			return nil, errors.Join(
				attachErr,
				fmt.Errorf("close event pipe: %w", closeErr),
			)
		}
		return nil, attachErr
	}
	return reader, nil
}

func retransmitAttachOptions(isTLPEnabled bool) []bpf.AttachOption {
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
