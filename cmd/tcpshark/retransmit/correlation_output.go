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
		event.CorrelationReason = result.reason
		event.NetNamespace = result.netNamespace
		switch result.reason {
		case types.CorrelationMatched:
			if result.drop == nil {
				return errors.Join(statusErr, fmt.Errorf(
					"correlation result %d: reason %q requires a drop", resultIndex, result.reason,
				))
			}
			metadata := &result.drop.metadata
			event.DropSource = metadata.Source
			event.DropReason = metadata.Reason
			event.DropReasonGroup = metadata.ReasonGroup
			event.DropLocation = metadata.Source
			if result.drop.stackDepth != 0 {
				frames := symbol.KsymStackStrs(
					result.drop.stackPCs[:result.drop.stackDepth],
					int(result.drop.stackDepth),
				)
				event.DropStack = strings.Join(frames, "\n")
			}
		case types.CorrelationUnsupported, types.CorrelationWarmup, types.CorrelationWaitTimeout,
			types.CorrelationQueueFull, types.CorrelationInterrupted:
			if result.drop != nil {
				return errors.Join(statusErr, fmt.Errorf(
					"correlation result %d: reason %q cannot include a drop", resultIndex, result.reason,
				))
			}
			event.DropLocation = "unknown"
			statusCopy := status
			event.DropPerfStatus = &statusCopy
		default:
			return errors.Join(statusErr, fmt.Errorf(
				"correlation result %d: invalid reason %q", resultIndex, result.reason,
			))
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
