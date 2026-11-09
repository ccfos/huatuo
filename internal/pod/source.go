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

package pod

import "context"

// Source returns one discovery attempt; only Complete authorizes deletions.
// Returned metadata becomes Manager-owned and must not be modified afterwards.
type Source interface {
	Type() string
	Discover(ctx context.Context) (Discovery, error)
}

// Discovery separates membership completeness from per-member readiness.
// A nil Pod retains the known ID without refreshing its cached metadata.
type Discovery struct {
	Pods       map[string]*Pod
	Complete   bool
	GuestPeers map[string]GuestPeer
}

// GuestPeer identifies the transport peer, not a collection process or netns.
type GuestPeer struct {
	PID int
}
