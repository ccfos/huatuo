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
	"context"
	"errors"
	"fmt"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

const procRoot = "/proc"

// Provider captures a HotSpot heap snapshot through the external reader.
type Provider struct{}

// New builds the production Java snapshot provider.
func New() *Provider {
	return new(Provider)
}

// Snapshot scans the victim HotSpot heap and reduces it to type aggregates.
// Unavailable or partial data is a snapshot; fatal read failures and cancellation
// return an error without a snapshot.
func (*Provider) Snapshot(ctx context.Context,
	request memsnapshot.Request,
) (*memsnapshot.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	snapshot, err := snapshot(ctx, request.Process, request.TopK,
		request.SamplingSeed)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	if err != nil {
		if errors.Is(err, errHotSpotUnavailable) {
			return memsnapshot.Unavailable(err.Error()), nil
		}
		return nil, fmt.Errorf("external HotSpot heap scan failed: %w", err)
	}
	if snapshot == nil {
		return nil, errors.New("Java external heap reader returned a nil snapshot")
	}
	return snapshot, nil
}
