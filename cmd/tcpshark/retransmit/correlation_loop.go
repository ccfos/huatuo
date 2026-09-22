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
	"context"
	"errors"
	"time"

	"github.com/ccfos/huatuo/pkg/types"
)

type retransmitDropSession struct {
	retransmitEvents    <-chan *retransmitEvent
	dropwatchEvents     <-chan *dropEvent
	readDropwatchStatus func() (types.DropwatchStatus, error)
	sink                writer
	sourceType          string
}

func runRetransmitDropCorrelation(
	ctx context.Context,
	session *retransmitDropSession,
) (returnErr error) {
	correlator, err := newEventCorrelator()
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(
			returnErr,
			session.emitResults(correlator.drainRetransmits(time.Now())),
		)
	}()

	timer := time.NewTimer(retransmitRetentionDuration)
	defer timer.Stop()

	for {
		if deadline, ok := correlator.nextDeadline(); ok {
			timer.Reset(time.Until(deadline))
		} else {
			timer.Stop()
		}
		var results []correlationResult
		select {
		case retransmit, ok := <-session.retransmitEvents:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("TCP retransmit event source closed unexpectedly")
			}
			results = correlator.processRetransmitEvent(
				retransmit,
				time.Now(),
			)
		case drop, ok := <-session.dropwatchEvents:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("embedded dropwatch event source closed unexpectedly")
			}
			results = correlator.processDropEvent(drop, time.Now())
		case <-timer.C:
			results = correlator.expireRetransmitPendingEvents(time.Now())
		case <-ctx.Done():
			return nil
		}
		if err := session.emitResults(results); err != nil {
			return err
		}
	}
}
