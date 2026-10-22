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

package pipeline

import (
	"fmt"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	"github.com/ccfos/huatuo/pkg/types"
)

// SampleBatch transfers all backing storage only when Enqueue succeeds.
// Providers must not reuse delivered batches or retry automatically.
type SampleBatch struct {
	Samples        []aggregation.Sample
	DroppedSamples map[string]uint64
}

// BatchBuilder bounds provider deliveries by both sample count and text size.
// It never reuses buffers after delivery and never retries rejected batches.
type BatchBuilder struct {
	batch     SampleBatch
	textBytes int
}

func (b *BatchBuilder) Append(sample *aggregation.Sample, enqueue func(*SampleBatch) error) error {
	size, err := SampleTextSize(sample)
	if err != nil {
		return err
	}

	if len(b.batch.Samples) == MaxBatchSamples || size > MaxBatchTextBytes-b.textBytes {
		if err := b.Flush(enqueue); err != nil {
			return err
		}
	}

	b.batch.Samples = append(b.batch.Samples, *sample)
	b.textBytes += size

	return nil
}

func (b *BatchBuilder) Flush(enqueue func(*SampleBatch) error) error {
	if len(b.batch.Samples) == 0 {
		return nil
	}

	batch := b.batch
	b.batch = SampleBatch{}
	b.textBytes = 0

	return enqueue(&batch)
}

func validateBatch(batch *SampleBatch) error {
	if batch == nil {
		return fmt.Errorf("profiling sample batch is required")
	}

	if len(batch.Samples) > MaxBatchSamples {
		return fmt.Errorf("profiling batch exceeds %d samples", MaxBatchSamples)
	}

	for reason := range batch.DroppedSamples {
		if reason != types.DropReasonCollection {
			return fmt.Errorf("provider cannot submit drop reason %q", reason)
		}
	}

	size := 0
	for i := range batch.Samples {
		n, err := SampleTextSize(&batch.Samples[i])
		if err != nil {
			return fmt.Errorf("profiling sample %d: %w", i, err)
		}

		if n > MaxBatchTextBytes-size {
			return fmt.Errorf("profiling batch text exceeds %d bytes", MaxBatchTextBytes)
		}

		size += n
	}

	return nil
}

// SampleTextSize also checks per-sample limits. Providers use the same limits
// when forming batches; Enqueue checks them again at the ownership boundary.
func SampleTextSize(sample *aggregation.Sample) (int, error) {
	if len(sample.UserFrames) > MaxSampleFrames ||
		len(sample.KernelFrames) > MaxSampleFrames ||
		len(sample.Labels) > MaxSampleLabels {
		return 0, fmt.Errorf("profiling sample exceeds frame or label limit")
	}

	size := 0
	for _, frames := range [][]types.ProfileFrame{sample.UserFrames, sample.KernelFrames} {
		for i := range frames {
			for _, s := range []string{frames[i].Name, frames[i].File} {
				if len(s) > MaxBatchTextBytes-size {
					return 0, fmt.Errorf("profiling sample text exceeds %d bytes", MaxBatchTextBytes)
				}

				size += len(s)
			}
		}
	}

	for i := range sample.Labels {
		label := &sample.Labels[i]
		for _, s := range []string{label.Name, label.StringValue, label.NumberUnit} {
			if len(s) > MaxBatchTextBytes-size {
				return 0, fmt.Errorf("profiling sample text exceeds %d bytes", MaxBatchTextBytes)
			}

			size += len(s)
		}
	}

	return size, nil
}
