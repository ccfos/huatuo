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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/prometheus/procfs"
)

func capture(ctx context.Context, opts *captureOptions) (returnErr error) {
	base, endpoints, err := parseCaptureURLs(opts)
	if err != nil {
		return err
	}

	if err := os.Mkdir(opts.directory, 0o700); err != nil {
		return err
	}
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = errors.Join(returnErr, os.RemoveAll(opts.directory))
	}()

	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
		TLSHandshakeTimeout: 2 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   opts.timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer transport.CloseIdleConnections()

	var target procfs.ProcStat
	if opts.pid != 0 {
		proc, err := procfs.NewProc(opts.pid)
		if err != nil {
			return err
		}

		target, err = proc.Stat()
		if err != nil {
			return err
		}

		readyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = waitReady(
			readyCtx,
			client,
			base,
			&target,
		)
		cancel()
		if err != nil {
			return err
		}
	}

	for _, endpoint := range endpoints {
		requestURL := *base
		requestURL.Path += strings.TrimPrefix(endpoint.Path, "/")
		requestURL.RawQuery = endpoint.RawQuery
		filename := filepath.Join(opts.directory, endpointFile(endpoint))
		file, err := os.OpenFile(filename+".partial", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}

		err = fetch(
			ctx,
			client,
			requestURL.String(),
			file,
			opts.maxBytes,
		)
		if err := errors.Join(err, file.Close()); err != nil {
			return fmt.Errorf(
				"capture %s to %s.partial: %w",
				endpoint,
				filename,
				err,
			)
		}

		if err := os.Rename(filename+".partial", filename); err != nil {
			return err
		}
	}

	if opts.pid != 0 {
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := checkTarget(
			checkCtx,
			client,
			base,
			&target,
		); err != nil {
			return err
		}
	}

	// Publish the endpoint list only after every request and target check succeeds.
	data, err := json.Marshal(opts.endpoints)
	if err != nil {
		return err
	}

	filename := filepath.Join(opts.directory, "capture.json")
	if err := os.WriteFile(filename+".partial", data, 0o600); err != nil {
		return err
	}

	return os.Rename(filename+".partial", filename)
}
