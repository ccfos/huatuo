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

package guestbridge

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"

	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/storage/vsock"
	"github.com/ccfos/huatuo/internal/timeutil"
	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"
	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
	"github.com/ccfos/huatuo/pkg/types"
)

var (
	ErrUntrustedPeer   = errors.New("guestbridge: untrusted peer")
	ErrUnsupportedType = errors.New("guestbridge: unsupported document type")
	tracerNamePattern  = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

// DocumentReceiver uses only Host-owned attribution and native storage paths.
type DocumentReceiver struct {
	LookupContainer func(string) (*pod.Container, error)
	Hostname        string
	Region          string
	Tracing         *tracingstore.Store
	Profiling       *profilingstore.Store
}

func (r *DocumentReceiver) attribute(ctx context.Context, d *types.Document) error {
	peer, ok := ctx.Value(peerIdentityKey{}).(PeerIdentity)
	if !ok || peer.SandboxID == "" {
		return ErrUntrustedPeer
	}
	if d.TracerID == "" || !tracerNamePattern.MatchString(d.TracerName) {
		return fmt.Errorf("guestbridge: tracer ID and safe tracer name are required")
	}
	d.ContainerHostname = ""
	d.ContainerHostNamespace = peer.Namespace
	d.ContainerType = ""
	d.ContainerQoS = ""
	if d.ContainerID != "" {
		lookup := r.LookupContainer
		if lookup == nil {
			lookup = pod.ContainerByID
		}
		container, err := lookup(d.ContainerID)
		if err != nil || container == nil || container.ID != d.ContainerID || container.HostKataSandboxID() != peer.SandboxID || container.Labels["HostNamespace"] != peer.Namespace {
			return ErrUntrustedPeer
		}
		d.ContainerHostname = container.Hostname
		d.ContainerType = container.Type.String()
		d.ContainerQoS = container.Qos.String()
	}
	d.TracerID = fmt.Sprintf("kata-%x", sha256.Sum256([]byte(peer.SandboxID+"\x00"+d.TracerID)))
	d.Hostname = r.Hostname
	d.Region = r.Region
	d.SourceType = types.DocumentSourceKataGuest
	d.UploadedTimestamp = timeutil.Now()
	return d.Validate()
}

// Handle accepts one document from an authenticated connection. Decoding keeps
// heterogeneous event numbers and typed profiling samples at full precision.
func (r *DocumentReceiver) Handle(ctx context.Context, tool string, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch tool {
	case vsock.TracingTool:
		d, err := tracingstore.DecodeDocument(raw)
		if err != nil {
			return err
		}
		if d.TracerRunType != types.TracerRunTypeEvent && d.TracerRunType != types.TracerRunTypeAutotracing && d.TracerRunType != types.TracerRunTypeTracing {
			return ErrUnsupportedType
		}
		if err := r.attribute(ctx, &d.Document); err != nil {
			return err
		}
		if r.Tracing == nil {
			return fmt.Errorf("guestbridge: tracing storage unavailable")
		}
		return r.Tracing.Save(d)
	case vsock.ProfilingTool:
		d, err := profilingstore.DecodeDocument(raw)
		if err != nil {
			return err
		}
		if d.TracerRunType != types.TracerRunTypeProfiling {
			return ErrUnsupportedType
		}
		if err := r.attribute(ctx, &d.Document); err != nil {
			return err
		}
		if r.Profiling == nil {
			return fmt.Errorf("guestbridge: profiling storage unavailable")
		}
		return r.Profiling.Save(ctx, d)
	default:
		return ErrUnsupportedType
	}
}
