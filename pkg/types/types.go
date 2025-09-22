// Copyright 2025 The HuaTuo Authors
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

package types

import "errors"

var (
	// ErrExitByCancelCtx defined an error canceled by context
	ErrExitByCancelCtx = errors.New("exit by cancelCtx")
	// ErrDisconnectedHuatuo defined an error that is disconnected to huatuo
	ErrDisconnectedHuatuo = errors.New("disconnected to huatuo")
	// ErrNotSupported indicates that a feature is not supported.
	ErrNotSupported = errors.New("not supported")
	// Not valid args for function
	ErrArgsInvalid = errors.New("args invalid")
)
