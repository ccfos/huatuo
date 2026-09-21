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
	"errors"
	"fmt"
	"strings"

	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/pkg/types"
)

func (s *retransmitDropSession) emitResults(results []correlationResult) error {
	if len(results) == 0 {
		return nil
	}
	status, statusErr := s.readDropwatchStatus()

	for resultIndex := range results {
		result := &results[resultIndex]
		event, err := result.retransmit.tracing(s.sourceType)
		if err != nil {
			return errors.Join(statusErr, err)
		}
		if result.drop == nil {
			event.DropLocation = "unknown"
			event.CorrelationReasons = append(
				[]types.CorrelationReason(nil),
				result.reasons...,
			)
			if statusErr == nil {
				statusCopy := status
				event.DropwatchPerfStatus = &statusCopy
			}
			if statusErr != nil {
				event.CorrelationReasons = append(
					event.CorrelationReasons,
					types.CorrelationReasonDropwatchPerfStatusUnavailable,
				)
			}
			// ReadStatus zeroes unavailable map counters and preserves reader loss.
			if status.RateLimited != 0 {
				event.CorrelationReasons = append(
					event.CorrelationReasons,
					types.CorrelationReasonDropRateLimited,
				)
			}
			if status.PerfLost != 0 || status.LostSamples != 0 {
				event.CorrelationReasons = append(
					event.CorrelationReasons,
					types.CorrelationReasonPerfEventsLost,
				)
			}
		} else {
			event.DropLocation = "host_software"
			if result.drop.stackDepth != 0 {
				frames := symbol.KsymStackStrs(
					result.drop.stackPCs[:result.drop.stackDepth],
					int(result.drop.stackDepth),
				)
				event.DropStack = strings.Join(frames, "\n")
			}
		}
		if err := s.sink.Write(event); err != nil {
			return errors.Join(
				statusErr,
				fmt.Errorf("write correlated TCP retransmit event: %w", err),
			)
		}
	}
	return statusErr
}
