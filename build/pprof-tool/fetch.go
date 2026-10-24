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

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/prometheus/procfs"
)

func waitReady(ctx context.Context, client *http.Client, base *url.URL, target *procfs.ProcStat) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		err := checkTarget(
			ctx,
			client,
			base,
			target,
		)
		if err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for PID %d pprof endpoint: %w", target.PID, err)
		case <-ticker.C:
		}
	}
}

// cmdline is a compatibility check, not proof of socket ownership when processes
// have identical arguments. The caller must still isolate concurrent listeners.
func checkTarget(ctx context.Context, client *http.Client, base *url.URL, target *procfs.ProcStat) error {
	proc, err := procfs.NewProc(target.PID)
	if err != nil {
		return err
	}

	stat, err := proc.Stat()
	if err != nil {
		return err
	}

	if stat.Starttime != target.Starttime {
		return fmt.Errorf("PID %d was reused", target.PID)
	}

	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", target.PID))
	if err != nil {
		return err
	}

	var body bytes.Buffer
	if err := fetch(
		ctx,
		client,
		base.String()+"cmdline",
		&body,
		int64(len(cmdline)),
	); err != nil {
		return err
	}

	if len(cmdline) == 0 || !bytes.Equal(bytes.TrimSuffix(cmdline, []byte{0}), body.Bytes()) {
		return fmt.Errorf("pprof cmdline does not match PID %d", target.PID)
	}

	return nil
}

func fetch(ctx context.Context, client *http.Client, address string, dst io.Writer, maxBytes int64) (returnErr error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		address,
		http.NoBody,
	)
	if err != nil {
		return err
	}

	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, response.Body.Close()) }()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP status %s", response.Status)
	}

	n, err := io.Copy(dst, io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return err
	}

	if n > maxBytes {
		return fmt.Errorf("response exceeds %d bytes", maxBytes)
	}

	return nil
}
