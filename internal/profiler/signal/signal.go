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

package signal

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
)

func SetupSignals() (chan os.Signal, error) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGTERM)
	return signals, nil
}

func ListenSignalAndCancel(sigCh <-chan os.Signal, cancel context.CancelFunc) (os.Signal, error) {
	sig, ok := <-sigCh
	if !ok {
		return nil, errors.New("signal channel closed unexpectedly")
	}
	cancel()
	return sig, nil
}
