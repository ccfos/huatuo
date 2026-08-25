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

	"github.com/ccfos/huatuo/internal/memsnap"
)

const procRoot = "/proc"

// Provider captures a HotSpot heap snapshot through the external reader.
type Provider struct{}

// NewProvider builds the production Java snapshot provider.
func NewProvider() *Provider {
	return new(Provider)
}

// Capture scans the victim HotSpot heap and reduces it to type aggregates.
func (*Provider) Capture(ctx context.Context,
	request memsnap.Request,
) (*memsnap.Snapshot, error) {
	snapshot, err := capture(ctx, request.Identity, request.TopK,
		request.SamplingSeed)
	if err != nil {
		if errors.Is(err, errHotSpotUnavailable) {
			return memsnap.Unavailable(err.Error()), nil
		}
		return memsnap.Failed(
			"external HotSpot heap scan failed: " + err.Error()), nil
	}
	if snapshot == nil {
		return memsnap.Failed("Java external heap reader returned a nil snapshot"), nil
	}
	return snapshot, nil
}
