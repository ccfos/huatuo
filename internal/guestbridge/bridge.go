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

// Package guestbridge receives tracing documents from Kata guests.
package guestbridge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/toolstream/transport"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	DefaultPort                       = 19705
	DefaultMaxConnections             = 512
	DefaultMaxMessageSize      uint64 = 16 << 20
	DefaultMaxInFlightBytes    uint64 = 256 << 20
	DefaultHandshakeTimeout           = 10 * time.Second
	DefaultFirecrackerRoot            = "/run/vc/firecracker"
	defaultFirecrackerInterval        = 2 * time.Second
	defaultListenerRetry              = time.Second
)

// Options configures Host GuestBridge listeners and the shared receiver.
type Options struct {
	// ResolvePeer uses Host metadata to authenticate a VSOCK address or a
	// Firecracker listener's sandbox ID before accepting any Guest document.
	ResolvePeer         func(net.Addr, string) (PeerIdentity, error)
	Port                uint32
	MaxConnections      int
	MaxMessageSize      uint64
	MaxInFlightBytes    uint64
	HandshakeTimeout    time.Duration
	FirecrackerRoot     string
	FirecrackerInterval time.Duration
	ListenerRetry       time.Duration
	QEMUListen          func(uint32) (net.Listener, error)
	UnixListen          func(string) (net.Listener, error)
	Receiver            *DocumentReceiver
	Registerer          prometheus.Registerer
}

type listenerEntry struct {
	server     *transport.Server
	socketPath string
	failed     <-chan struct{}
}

type bridgeShutdown struct {
	done chan struct{}
	err  error
}

type failureListener struct {
	net.Listener
	failed    chan struct{}
	closed    chan struct{}
	failOnce  sync.Once
	closeOnce sync.Once
	closeErr  error
}

func newFailureListener(listener net.Listener) *failureListener {
	return &failureListener{
		Listener: listener,
		failed:   make(chan struct{}),
		closed:   make(chan struct{}),
	}
}

func (listener *failureListener) Accept() (net.Conn, error) {
	select {
	case <-listener.failed:
		<-listener.closed
		return nil, net.ErrClosed
	default:
	}

	connection, err := listener.Listener.Accept()
	if err != nil {
		listener.failOnce.Do(func() {
			close(listener.failed)
		})
	}
	return connection, err
}

func (listener *failureListener) Close() error {
	listener.closeOnce.Do(func() {
		listener.closeErr = listener.Listener.Close()
		close(listener.closed)
	})
	return listener.closeErr
}

// Bridge owns all QEMU and Firecracker listeners and their active connections.
type Bridge struct {
	resolvePeer         func(net.Addr, string) (PeerIdentity, error)
	port                uint32
	maxMessageSize      uint64
	handshakeTimeout    time.Duration
	decodeBudget        *transport.DecodeBudget
	firecrackerRoot     string
	firecrackerInterval time.Duration
	listenerRetry       time.Duration
	qemuListen          func(uint32) (net.Listener, error)
	unixListen          func(string) (net.Listener, error)
	receiver            *DocumentReceiver
	metrics             *bridgeMetrics
	limiter             *connectionLimiter

	mutex     sync.Mutex
	listeners map[string]*listenerEntry
	cancel    context.CancelFunc
	done      sync.WaitGroup
	started   bool
	shutdown  *bridgeShutdown
}

// New creates a disabled-until-Start GuestBridge.
//
//nolint:gocritic // Options is a one-time immutable startup value.
func New(options Options) (*Bridge, error) {
	if options.ResolvePeer == nil {
		options.ResolvePeer = resolvePeer
	}
	if options.Receiver == nil {
		return nil, fmt.Errorf("guestbridge: document receiver is required")
	}
	if options.Port == 0 {
		options.Port = DefaultPort
	}
	if options.MaxConnections < 0 || options.HandshakeTimeout < 0 || options.FirecrackerInterval < 0 || options.ListenerRetry < 0 || options.Port == ^uint32(0) {
		return nil, fmt.Errorf("guestbridge: invalid limits, port or timeouts")
	}
	if options.MaxConnections == 0 {
		options.MaxConnections = DefaultMaxConnections
	}
	if options.MaxMessageSize == 0 {
		options.MaxMessageSize = DefaultMaxMessageSize
	}
	if options.MaxInFlightBytes == 0 {
		options.MaxInFlightBytes = DefaultMaxInFlightBytes
	}
	if options.MaxMessageSize < 128 {
		return nil, fmt.Errorf("guestbridge: max message size must be at least 128 bytes")
	}
	if options.MaxInFlightBytes < options.MaxMessageSize {
		return nil, fmt.Errorf(
			"guestbridge: max in-flight bytes %d is smaller than max message size %d",
			options.MaxInFlightBytes,
			options.MaxMessageSize,
		)
	}
	if options.HandshakeTimeout <= 0 {
		options.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if options.FirecrackerRoot == "" {
		options.FirecrackerRoot = DefaultFirecrackerRoot
	}
	if options.FirecrackerInterval <= 0 {
		options.FirecrackerInterval = defaultFirecrackerInterval
	}
	if options.ListenerRetry <= 0 {
		options.ListenerRetry = defaultListenerRetry
	}
	if options.QEMUListen == nil {
		options.QEMUListen = listenQEMU
	}
	if options.UnixListen == nil {
		options.UnixListen = listenFirecracker
	}
	decodeBudget, err := transport.NewDecodeBudget(options.MaxInFlightBytes)
	if err != nil {
		return nil, fmt.Errorf("guestbridge: %w", err)
	}

	metrics, err := newBridgeMetrics(options.Registerer)
	if err != nil {
		return nil, err
	}
	bridge := &Bridge{
		resolvePeer:         options.ResolvePeer,
		port:                options.Port,
		maxMessageSize:      options.MaxMessageSize,
		handshakeTimeout:    options.HandshakeTimeout,
		decodeBudget:        decodeBudget,
		firecrackerRoot:     options.FirecrackerRoot,
		firecrackerInterval: options.FirecrackerInterval,
		listenerRetry:       options.ListenerRetry,
		qemuListen:          options.QEMUListen,
		unixListen:          options.UnixListen,
		receiver:            options.Receiver,
		metrics:             metrics,
		listeners:           make(map[string]*listenerEntry),
	}
	bridge.limiter = newConnectionLimiter(options.MaxConnections, metrics.connections)
	return bridge, nil
}

// Start immediately begins QEMU listener retries and Firecracker scans.
func (bridge *Bridge) Start() error {
	bridge.mutex.Lock()
	defer bridge.mutex.Unlock()
	if bridge.started {
		return fmt.Errorf("guestbridge: already started")
	}
	if bridge.shutdown != nil {
		select {
		case <-bridge.shutdown.done:
			bridge.shutdown = nil
		default:
			return fmt.Errorf("guestbridge: shutdown in progress")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	bridge.cancel = cancel
	bridge.started = true
	bridge.done.Add(2)
	go func() {
		defer bridge.done.Done()
		bridge.runQEMU(ctx)
	}()
	go func() {
		defer bridge.done.Done()
		bridge.runFirecracker(ctx)
	}()
	return nil
}

func (bridge *Bridge) runQEMU(ctx context.Context) {
	for ctx.Err() == nil {
		listener, err := bridge.qemuListen(bridge.port)
		if err != nil {
			bridge.metrics.listenerUp.WithLabelValues("qemu").Set(0)
			log.Warnf("guestbridge: qemu listen: %v", err)
			if !waitContext(ctx, bridge.listenerRetry) {
				return
			}
			continue
		}

		entry, err := bridge.serve(listener, "qemu", "")
		if err != nil {
			_ = listener.Close()
			bridge.metrics.listenerUp.WithLabelValues("qemu").Set(0)
			log.Warnf("guestbridge: qemu serve: %v", err)
			if !waitContext(ctx, bridge.listenerRetry) {
				return
			}
			continue
		}
		if !bridge.addListener("qemu", entry) {
			_ = entry.server.Close()
			return
		}
		bridge.metrics.listenerUp.WithLabelValues("qemu").Set(1)
		select {
		case <-ctx.Done():
			return
		case <-entry.failed:
			bridge.metrics.listenerUp.WithLabelValues("qemu").Set(0)
			bridge.removeListener("qemu", entry)
			log.Warnf("guestbridge: qemu listener stopped; retrying")
			if !waitContext(ctx, bridge.listenerRetry) {
				return
			}
		}
	}
}

func (bridge *Bridge) runFirecracker(ctx context.Context) {
	bridge.metrics.listenerUp.WithLabelValues("firecracker").Set(1)
	defer bridge.metrics.listenerUp.WithLabelValues("firecracker").Set(0)

	bridge.scanFirecracker()
	ticker := time.NewTicker(bridge.firecrackerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			bridge.scanFirecracker()
		}
	}
}

func (bridge *Bridge) scanFirecracker() {
	entries, err := os.ReadDir(bridge.firecrackerRoot)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Warnf("guestbridge: scan firecracker root: %v", err)
		}
		return
	}

	desired := make(map[string]string)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		basePath := filepath.Join(
			bridge.firecrackerRoot,
			entry.Name(),
			"root",
			"kata.hvsock",
		)
		if _, err := os.Stat(basePath); err != nil {
			continue
		}
		desired[entry.Name()] = basePath + "_" + strconv.FormatUint(uint64(bridge.port), 10)
	}

	bridge.mutex.Lock()
	existing := make(map[string]*listenerEntry)
	failed := make(map[string]*listenerEntry)
	for key, entry := range bridge.listeners {
		if strings.HasPrefix(key, "firecracker:") {
			select {
			case <-entry.failed:
				failed[key] = entry
			default:
				existing[key] = entry
			}
		}
	}
	bridge.mutex.Unlock()
	for key, entry := range failed {
		bridge.removeListener(key, entry)
	}

	for sandboxID, socketPath := range desired {
		key := "firecracker:" + sandboxID
		if _, ok := existing[key]; ok {
			delete(existing, key)
			continue
		}

		if err := removeStaleSocket(socketPath); err != nil {
			log.Warnf("guestbridge: remove stale socket %s: %v", socketPath, err)
			continue
		}
		listener, err := bridge.unixListen(socketPath)
		if err != nil {
			log.Warnf("guestbridge: firecracker listen sandbox=%s: %v", sandboxID, err)
			continue
		}
		listenerEntry, err := bridge.serve(listener, "firecracker", sandboxID)
		if err != nil {
			_ = listener.Close()
			_ = os.Remove(socketPath)
			log.Warnf("guestbridge: firecracker serve sandbox=%s: %v", sandboxID, err)
			continue
		}
		listenerEntry.socketPath = socketPath
		if !bridge.addListener(key, listenerEntry) {
			_ = listenerEntry.server.Close()
			_ = os.Remove(socketPath)
			return
		}
	}

	for key, entry := range existing {
		bridge.removeListener(key, entry)
	}
}

func (bridge *Bridge) serve(
	listener net.Listener,
	transportName string,
	sandboxID string,
) (*listenerEntry, error) {
	failureListener := newFailureListener(bridge.limiter.wrap(listener))
	server, err := transport.ServeWithOptions(
		failureListener, bridge.handleDocument,
		transport.ServerOptions{
			ConnContext: func(ctx context.Context, conn net.Conn) (context.Context, error) {
				peer, err := bridge.waitConnectionPeer(ctx, conn, sandboxID)
				if err != nil {
					log.Warnf("guestbridge: reject peer=%v sandbox=%s: %v", conn.RemoteAddr(), sandboxID, err)
					return nil, err
				}
				return context.WithValue(ctx, peerIdentityKey{}, peer), nil
			},
			MaxMessageSize:   bridge.maxMessageSize,
			HandshakeTimeout: bridge.handshakeTimeout,
			DecodeBudget:     bridge.decodeBudget,
			OnReceiveError: func(err error) {
				bridge.metrics.receiveErrors.WithLabelValues(receiveErrorReason(err)).Inc()
			},
		},
	)
	if err != nil {
		return nil, err
	}
	log.Infof("guestbridge: listener started transport=%s addr=%s", transportName, listener.Addr())
	return &listenerEntry{server: server, failed: failureListener.failed}, nil
}

func (bridge *Bridge) handleDocument(session *transport.Session, chunk transport.ChunkMsg) {
	if len(chunk.Data) == 0 {
		return
	}
	started := time.Now()
	defer func() { bridge.metrics.ingestDuration.Observe(time.Since(started).Seconds()) }()
	if err := bridge.receiver.Handle(session.Context, session.ToolName, chunk.Data); err != nil {
		bridge.metrics.dropped.WithLabelValues(documentDropReason(err)).Inc()
		log.Warnf("guestbridge: rejected document: %v", err)
		return
	}
	bridge.metrics.received.Inc()
}

func (bridge *Bridge) addListener(key string, entry *listenerEntry) bool {
	bridge.mutex.Lock()
	defer bridge.mutex.Unlock()
	if !bridge.started || bridge.cancel == nil {
		return false
	}
	bridge.listeners[key] = entry
	return true
}

func (bridge *Bridge) removeListener(key string, entry *listenerEntry) {
	bridge.mutex.Lock()
	delete(bridge.listeners, key)
	bridge.mutex.Unlock()
	_ = entry.server.Close()
	if entry.socketPath != "" {
		_ = os.Remove(entry.socketPath)
	}
}

// Close stops scanning and closes every Listener and active connection.
func (bridge *Bridge) Close() error {
	return bridge.Shutdown(context.Background())
}

// Shutdown stops listeners and cancels their handlers, waiting at most until ctx
// expires. If it times out, storage must remain open while handlers finish.
func (bridge *Bridge) Shutdown(ctx context.Context) error {
	bridge.mutex.Lock()
	if !bridge.started && bridge.shutdown == nil {
		bridge.mutex.Unlock()
		return nil
	}
	if bridge.shutdown == nil {
		bridge.started = false
		bridge.cancel()
		listeners := bridge.listeners
		bridge.listeners = make(map[string]*listenerEntry)
		shutdown := &bridgeShutdown{done: make(chan struct{})}
		bridge.shutdown = shutdown
		go func() {
			shutdown.err = bridge.closeListeners(listeners)
			close(shutdown.done)
		}()
	}
	shutdown := bridge.shutdown
	bridge.mutex.Unlock()

	select {
	case <-shutdown.done:
		return shutdown.err
	default:
	}
	select {
	case <-shutdown.done:
		return shutdown.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (bridge *Bridge) closeListeners(listeners map[string]*listenerEntry) error {
	// Cancel every listener promptly even if another listener has a stuck handler.
	results := make(chan error, len(listeners))
	for _, entry := range listeners {
		go func() {
			err := entry.server.Close()
			if entry.socketPath != "" {
				if removeErr := os.Remove(entry.socketPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					err = errors.Join(err, removeErr)
				}
			}
			results <- err
		}()
	}
	var errs []error
	for range listeners {
		errs = append(errs, <-results)
	}
	bridge.done.Wait()
	bridge.metrics.listenerUp.WithLabelValues("qemu").Set(0)
	return errors.Join(errs...)
}

func documentDropReason(err error) string {
	switch {
	case errors.Is(err, ErrUntrustedPeer):
		return "untrusted_peer"
	case errors.Is(err, ErrUnsupportedType):
		return "unsupported_type"
	default:
		return "invalid_document"
	}
}

func receiveErrorReason(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "too large"):
		return "oversize"
	case strings.Contains(message, "protocol version"):
		return "unsupported_protocol"
	default:
		return "invalid_frame"
	}
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
