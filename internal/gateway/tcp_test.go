package gateway

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/state"
)

func TestTCPActivationPinsConnectionAndPreservesBytes(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	served := make(chan struct{})
	go func() {
		defer close(served)
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(conn, conn)
	}()
	_, port, _ := net.SplitHostPort(backend.Addr().String())
	n, _ := strconv.Atoi(port)
	s := state.NewPodStore()
	s.Apply(manifest.PodSpec{Name: "tcp", OnDemand: &manifest.OnDemandSpec{Mode: "tcp", HostPort: n, GatewayPort: 0, StartupTimeout: time.Second, IdleTimeout: time.Second, MaxRequests: 4}})
	wake := make(chan struct{}, 1)
	m := NewTCPManager(s, func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	m.sync(ctx)
	defer func() {
		cancel()
		for _, b := range m.bindings {
			b.listener.Close()
		}
		m.wg.Wait()
	}()
	b, ok := m.bindings["tcp"]
	if !ok {
		t.Fatal("listener missing")
	}
	client, err := net.Dial("tcp", b.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("no activation")
	}
	s.UpdateStatus("tcp", state.StatusRunning, "started")
	s.SetReady("tcp", true)
	client.SetDeadline(time.Now().Add(2 * time.Second))
	payload := []byte{0, 1, 2, 255, 13, 10}
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != string(payload) {
		t.Fatal("stream changed")
	}
	if s.TryIdleStop("tcp", time.Now().Add(time.Hour)) {
		t.Fatal("evicted open TCP connection")
	}
	client.(*net.TCPConn).CloseWrite()
	io.Copy(io.Discard, client)
	select {
	case <-served:
	case <-time.After(time.Second):
		t.Fatal("half close not forwarded")
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		p, _ := s.Get("tcp")
		if p.InFlight == 0 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("lease leaked")
		case <-tick.C:
		}
	}
	if !s.TryIdleStop("tcp", time.Now().Add(time.Hour)) {
		t.Fatal("closed stream blocked idle eviction")
	}
}

func TestTCPShutdownReleasesWaitingConnection(t *testing.T) {
	s := state.NewPodStore()
	s.Apply(manifest.PodSpec{Name: "tcp", OnDemand: &manifest.OnDemandSpec{Mode: "tcp", GatewayPort: 0, StartupTimeout: time.Minute, MaxRequests: 1}})
	ctx, cancel := context.WithCancel(context.Background())
	m := NewTCPManager(s, nil)
	m.sync(ctx)
	conn, err := net.Dial("tcp", m.bindings["tcp"].listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()
	for _, b := range m.bindings {
		b.listener.Close()
	}
	m.wg.Wait()
	p, _ := s.Get("tcp")
	if p.InFlight != 0 {
		t.Fatal("shutdown leaked waiting connection")
	}
}
