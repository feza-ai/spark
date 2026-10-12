// Package gateway provides connection-triggered workload activation.
package gateway

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/feza-ai/spark/internal/state"
)

type binding struct {
	listener   net.Listener
	generation uint64
	port       int
}

// TCPManager owns stable public listeners while pods use separate backend ports.
// Connections retain a workload lease until both directions have finished.
type TCPManager struct {
	store      *state.PodStore
	wake       func()
	bindings   map[string]binding
	lastErrors map[string]string
	wg         sync.WaitGroup
}

func NewTCPManager(store *state.PodStore, wake func()) *TCPManager {
	return &TCPManager{store: store, wake: wake, bindings: make(map[string]binding), lastErrors: make(map[string]string)}
}

func (m *TCPManager) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer func() {
		for _, b := range m.bindings {
			b.listener.Close()
		}
		m.wg.Wait()
	}()
	for {
		m.sync(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *TCPManager) sync(ctx context.Context) {
	wanted := make(map[string]state.PodRecord)
	for _, pod := range m.store.List("") {
		if pod.Spec.OnDemand != nil && pod.Spec.OnDemand.ActivationMode() == "tcp" {
			wanted[pod.Spec.Name] = pod
		}
	}
	for name, b := range m.bindings {
		p, ok := wanted[name]
		if !ok || p.Generation != b.generation || p.Spec.OnDemand.GatewayPort != b.port {
			b.listener.Close()
			delete(m.bindings, name)
			delete(m.lastErrors, name)
		}
	}
	for name, pod := range wanted {
		if _, ok := m.bindings[name]; ok {
			continue
		}
		listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(pod.Spec.OnDemand.GatewayPort)))
		if err != nil {
			if m.lastErrors[name] != err.Error() {
				m.lastErrors[name] = err.Error()
				m.store.AddEvent(name, "GatewayBindFailed", err.Error())
				slog.Error("TCP gateway bind failed", "pod", name, "error", err)
			}
			continue
		}
		delete(m.lastErrors, name)
		m.bindings[name] = binding{listener: listener, generation: pod.Generation, port: pod.Spec.OnDemand.GatewayPort}
		m.store.AddEvent(name, "GatewayListening", fmt.Sprintf("TCP port %d", pod.Spec.OnDemand.GatewayPort))
		m.wg.Add(1)
		go func(p state.PodRecord, ln net.Listener) { defer m.wg.Done(); m.accept(ctx, p, ln) }(pod, listener)
	}
}

func (m *TCPManager) accept(ctx context.Context, pod state.PodRecord, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		m.wg.Add(1)
		go func() { defer m.wg.Done(); m.serve(ctx, pod, conn) }()
	}
}

func (m *TCPManager) serve(ctx context.Context, bound state.PodRecord, client net.Conn) {
	defer client.Close()
	stopClose := context.AfterFunc(ctx, func() { client.Close() })
	defer stopClose()
	pod, release, err := m.store.AcquireRequest(bound.Spec.Name)
	if err != nil {
		return
	}
	defer release()
	if pod.Generation != bound.Generation || pod.Spec.OnDemand.ActivationMode() != "tcp" {
		return
	}
	if m.wake != nil && (pod.Status != state.StatusRunning || !pod.Ready) {
		m.wake()
	}
	startup, cancel := context.WithTimeout(ctx, pod.Spec.OnDemand.StartupTimeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if startup.Err() != nil {
			return
		}
		current, ok := m.store.Get(pod.Spec.Name)
		if !ok || current.Generation != pod.Generation || current.Spec.OnDemand == nil || current.Spec.OnDemand.ActivationMode() != "tcp" {
			return
		}
		if current.Status == state.StatusFailed || current.Status == state.StatusCompleted {
			return
		}
		if current.Status == state.StatusRunning && current.Ready {
			dialer := net.Dialer{Timeout: 5 * time.Second}
			backend, err := dialer.DialContext(startup, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(current.Spec.OnDemand.HostPort)))
			if err != nil {
				return
			}
			defer backend.Close()
			stopBackend := context.AfterFunc(ctx, func() { backend.Close() })
			defer stopBackend()
			proxyDuplex(client, backend)
			return
		}
		select {
		case <-startup.Done():
			return
		case <-ticker.C:
		}
	}
}

func proxyDuplex(a, b net.Conn) {
	var wg sync.WaitGroup
	copyHalf := func(dst, src net.Conn) {
		defer wg.Done()
		if _, err := io.Copy(dst, src); err != nil {
			a.Close()
			b.Close()
			return
		}
		if tcp, ok := dst.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
	}
	wg.Add(2)
	go copyHalf(a, b)
	go copyHalf(b, a)
	wg.Wait()
}
