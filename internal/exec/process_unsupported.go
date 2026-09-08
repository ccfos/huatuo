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

//go:build !linux

package exec

import (
	"errors"
	osexec "os/exec"
)

var errUnsupportedPlatform = errors.New("exec: process management requires linux")

func configureCommand(_ *osexec.Cmd) error {
	return errUnsupportedPlatform
}

func gracefulStopProcessGroup(_ int) error {
	return errUnsupportedPlatform
}

func forceStopProcessGroup(_ int) error {
	return errUnsupportedPlatform
}

func processGroupMissing(_ error) bool {
	return false
}

func isStoppedExit(_ error) bool {
	return false
}
