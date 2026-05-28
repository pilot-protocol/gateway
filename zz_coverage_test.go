// SPDX-License-Identifier: AGPL-3.0-or-later

package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pilot-protocol/common/coreapi"
	"github.com/pilot-protocol/common/protocol"
)

// ---------------------------------------------------------------------------
// Test dialers
// ---------------------------------------------------------------------------

// pipeDialerSync hands out net.Pipe pairs and records every Close call.
type pipeDialerSync struct {
	mu       sync.Mutex
	peers    []net.Conn
	closed   int
	dialErr  error
	dialCnt  int
}

func (d *pipeDialerSync) DialAddr(protocol.Addr, uint16) (net.Conn, error) {
	d.mu.Lock()
	d.dialCnt++
	d.mu.Unlock()
	if d.dialErr != nil {
		return nil, d.dialErr
	}
	a, b := net.Pipe()
	d.mu.Lock()
	d.peers = append(d.peers, b)
	d.mu.Unlock()
	return a, nil
}

func (d *pipeDialerSync) Close() error {
	d.mu.Lock()
	d.closed++
	peers := d.peers
	d.peers = nil
	d.mu.Unlock()
	for _, p := range peers {
		_ = p.Close()
	}
	return nil
}

// errDialer always fails DialAddr — used to drive bridgeConnection's
// dial-error branch and Map's start-proxy path under a guaranteed-fail dial.
type errDialer struct{ closeErr error }

func (e *errDialer) DialAddr(protocol.Addr, uint16) (net.Conn, error) {
	return nil, errors.New("dial refused")
}
func (e *errDialer) Close() error { return e.closeErr }

// ---------------------------------------------------------------------------
// New error path — invalid subnet
// ---------------------------------------------------------------------------

func TestNew_InvalidSubnet(t *testing.T) {
	t.Parallel()
	gw, err := New(Config{Subnet: "not-a-cidr"}, nil)
	if err == nil {
		t.Fatal("expected error for invalid subnet")
	}
	if gw != nil {
		t.Fatalf("expected nil gateway on error, got %v", gw)
	}
}

func TestNew_DefaultsApplied(t *testing.T) {
	t.Parallel()
	gw, err := New(Config{}, nil) // empty subnet, empty ports
	if err != nil {
		t.Fatalf("New with empty config: %v", err)
	}
	if gw == nil {
		t.Fatal("expected non-nil gateway")
	}
	// Default subnet is 10.4.0.0/16; verify we can map within it.
	ip, err := gw.Mappings().Map(protocol.Addr{Network: 1, Node: 1}, nil)
	if err != nil {
		t.Fatalf("Map after default subnet: %v", err)
	}
	if !strings.HasPrefix(ip.String(), "10.4.") {
		t.Fatalf("default subnet not 10.4.x: got %s", ip)
	}
	// Default ports = DefaultPorts (8 entries).
	if got := len(gw.config.Ports); got != len(DefaultPorts) {
		t.Fatalf("default ports: got %d, want %d", got, len(DefaultPorts))
	}
}

// ---------------------------------------------------------------------------
// Stop — full lifecycle including dialer.Close, alias removal, idempotency
// ---------------------------------------------------------------------------

func TestStop_IdempotentAndClosesDialer(t *testing.T) {
	t.Parallel()
	d := &pipeDialerSync{}
	gw, err := New(Config{Subnet: "10.4.0.0/16", Ports: []uint16{}}, d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.Stop()
	// Second Stop must be a no-op (no panic, no extra Close on dialer).
	gw.Stop()
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed != 1 {
		t.Fatalf("dialer.Close called %d times, want 1", closed)
	}
}

func TestStop_NilDialerSafe(t *testing.T) {
	t.Parallel()
	gw, err := New(Config{Subnet: "10.4.0.0/16"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Should not panic when dialer is nil.
	gw.Stop()
}

func TestStop_RemovesAliasesWithStub(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; refuse to touch real loopback aliases")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("alias stub uses ip/ifconfig — skip on %s", runtime.GOOS)
	}
	withExecStubs(t, true)

	d := &pipeDialerSync{}
	gw, err := New(Config{Subnet: "10.4.0.0/16", Ports: []uint16{}}, d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Inject aliases as if startProxy had run.
	gw.mu.Lock()
	gw.aliases = append(gw.aliases, net.ParseIP("10.4.0.1"), net.ParseIP("10.4.0.2"))
	gw.mu.Unlock()

	gw.Stop()
	// aliases slice must be cleared and dialer closed.
	gw.mu.Lock()
	leftover := len(gw.aliases)
	gw.mu.Unlock()
	if leftover != 0 {
		t.Fatalf("aliases not cleared: %d", leftover)
	}
}

// ---------------------------------------------------------------------------
// Unmap — exercise listener cleanup + alias removal under stub PATH
// ---------------------------------------------------------------------------

func TestUnmap_ClosesListenerAndRemovesAlias(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; refuse to touch real loopback aliases")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("alias stub uses ip/ifconfig — skip on %s", runtime.GOOS)
	}
	withExecStubs(t, true)

	gw, err := New(Config{Subnet: "10.4.0.0/16", Ports: []uint16{}}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	addr := protocol.Addr{Network: 1, Node: 7}
	ip, err := gw.Mappings().Map(addr, nil)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}

	// Create a real TCP listener bound to 127.0.0.1 and inject it under the
	// matching IP key so Unmap finds it and Close()s it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	key := ip.String() + ":" + itoaCov(uint16(port))
	gw.mu.Lock()
	gw.listeners[key] = ln
	gw.aliases = append(gw.aliases, ip)
	gw.mu.Unlock()

	if err := gw.Unmap(ip.String()); err != nil {
		t.Fatalf("Unmap: %v", err)
	}
	// Listener must be gone.
	gw.mu.Lock()
	_, stillThere := gw.listeners[key]
	aliasN := len(gw.aliases)
	gw.mu.Unlock()
	if stillThere {
		t.Fatal("listener not removed by Unmap")
	}
	if aliasN != 0 {
		t.Fatalf("alias not removed: %d remain", aliasN)
	}
	// Listener should actually be closed (Accept returns immediately).
	if _, err := ln.Accept(); err == nil {
		t.Fatal("expected Accept to fail on closed listener")
	}
}

func TestUnmap_BadSplitHostPortKeySkipped(t *testing.T) {
	// No t.Parallel — withExecStubs uses t.Setenv which is incompatible.
	gw, err := New(Config{Subnet: "10.4.0.0/16"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	addr := protocol.Addr{Network: 1, Node: 11}
	ip, err := gw.Mappings().Map(addr, nil)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	// Inject a malformed listener key — Unmap must skip it without panicking.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	gw.mu.Lock()
	gw.listeners["this-is-not-host-port"] = ln
	gw.mu.Unlock()

	// Run Unmap; bad key remains untouched, mapping is still removed.
	// addLoopbackAlias is not invoked because there's no matching alias,
	// but removeLoopbackAlias IS invoked at the end — guarded by stub or
	// skipped under root.
	if os.Getuid() == 0 {
		t.Skip("would shell out as root")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("skip on %s", runtime.GOOS)
	}
	withExecStubs(t, true)
	if err := gw.Unmap(ip.String()); err != nil {
		t.Fatalf("Unmap: %v", err)
	}
	gw.mu.Lock()
	_, stillThere := gw.listeners["this-is-not-host-port"]
	gw.mu.Unlock()
	if !stillThere {
		t.Fatal("malformed-key listener should have been left in place")
	}
}

// ---------------------------------------------------------------------------
// Map — drives startProxy → addLoopbackAlias → listenPort under stub PATH
// ---------------------------------------------------------------------------

func TestMap_StartsProxyWithStubs(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; refuse to touch real loopback aliases")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("alias stub uses ip/ifconfig — skip on %s", runtime.GOOS)
	}
	withExecStubs(t, true)

	// Bypass the privilege check so the test can exercise the real path
	// through addLoopbackAlias without requiring root.
	old := loopbackPrivilegeCheck
	loopbackPrivilegeCheck = func() error { return nil }
	defer func() { loopbackPrivilegeCheck = old }()

	// Use ports=[] so listenPort never actually attempts a bind — we only
	// need to exercise Map → startProxy → addLoopbackAlias. (The bind path
	// is already covered by TestListenPort_AcceptsAndBridges.)
	d := &pipeDialerSync{}
	gw, err := New(Config{Subnet: "10.77.0.0/16", Ports: []uint16{}}, d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer gw.Stop()

	addr := protocol.Addr{Network: 1, Node: 99}
	ip, err := gw.Map(addr, "")
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if !strings.HasPrefix(ip.String(), "10.77.") {
		t.Fatalf("auto-assigned IP outside subnet: %s", ip)
	}

	// Give startProxy a moment to register the alias.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		gw.mu.Lock()
		n := len(gw.aliases)
		gw.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	gw.mu.Lock()
	n := len(gw.aliases)
	gw.mu.Unlock()
	if n == 0 {
		t.Fatal("startProxy did not register an alias")
	}
}

func TestMap_ExplicitIPWithStubs(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("skip on %s", runtime.GOOS)
	}
	withExecStubs(t, true)

	old := loopbackPrivilegeCheck
	loopbackPrivilegeCheck = func() error { return nil }
	defer func() { loopbackPrivilegeCheck = old }()

	d := &pipeDialerSync{}
	gw, err := New(Config{Subnet: "10.66.0.0/16", Ports: []uint16{}}, d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer gw.Stop()

	addr := protocol.Addr{Network: 1, Node: 5}
	ip, err := gw.Map(addr, "10.66.0.42")
	if err != nil {
		t.Fatalf("Map explicit: %v", err)
	}
	if ip.String() != "10.66.0.42" {
		t.Fatalf("expected 10.66.0.42, got %s", ip)
	}
	// Wait for startProxy goroutine to register the alias so the test
	// doesn't return (and t.TempDir clean up the stubs) while
	// addLoopbackAlias is still mid-exec.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		gw.mu.Lock()
		n := len(gw.aliases)
		gw.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMap_InvalidIP(t *testing.T) {
	t.Parallel()
	gw, err := New(Config{Subnet: "10.66.0.0/16", Ports: []uint16{}}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := gw.Map(protocol.Addr{Network: 1, Node: 1}, "not-an-ip"); err == nil {
		t.Fatal("expected error for invalid IP")
	}
}

func TestMap_PropagatesMappingError(t *testing.T) {
	t.Parallel()
	gw, err := New(Config{Subnet: "10.66.0.0/16", Ports: []uint16{}}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := gw.Map(protocol.Addr{Network: 1, Node: 1}, "192.168.1.1"); err == nil {
		t.Fatal("expected error for IP outside subnet")
	}
}

// ---------------------------------------------------------------------------
// addLoopbackAlias / removeLoopbackAlias — exercise via stub PATH plus the
// "unsupported OS" branch via direct call after temporarily swapping the
// process PATH so any accidental exec is a no-op.
// ---------------------------------------------------------------------------

func TestAddRemoveLoopbackAlias_StubExec(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; would touch real loopback aliases")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("alias helpers only support linux/darwin; running on %s", runtime.GOOS)
	}
	withExecStubs(t, true)

	gw, err := New(Config{Subnet: "10.4.0.0/16", Ports: []uint16{}}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ip := net.ParseIP("10.4.0.123")

	// Both must complete without panic. Errors from the stub (rc=0) are
	// expected to be nil; logging is the only side effect we observe.
	// The function now returns an error; with stubs it should succeed.
	if err := gw.addLoopbackAlias(ip); err != nil {
		t.Fatalf("addLoopbackAlias with successful stub: %v", err)
	}
	gw.removeLoopbackAlias(ip)
}

func TestAddRemoveLoopbackAlias_StubExecFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("skip on %s", runtime.GOOS)
	}
	// rc=1 stub — drives the err != nil branch.
	withExecStubs(t, false)

	gw, err := New(Config{Subnet: "10.4.0.0/16"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ip := net.ParseIP("10.4.0.200")
	if err := gw.addLoopbackAlias(ip); err != nil {
		t.Logf("addLoopbackAlias (rc=1 stub) returned error: %v", err)
	}
	gw.removeLoopbackAlias(ip) // logs error
}

// ---------------------------------------------------------------------------
// bridgeConnection — additional copy-direction error branches
// ---------------------------------------------------------------------------

func TestBridgeConnection_PilotReadErrorClosesTCP(t *testing.T) {
	t.Parallel()
	d := &pipeDialerSync{}
	gw, err := New(Config{Subnet: "10.4.0.0/16", Ports: []uint16{}}, d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tcpEnd, tcpPeer := net.Pipe()
	defer tcpEnd.Close()
	defer tcpPeer.Close()

	doneCh := make(chan struct{})
	go func() {
		gw.bridgeConnection(tcpEnd, protocol.Addr{Network: 1, Node: 1}, 80)
		close(doneCh)
	}()

	// Wait for dial to publish the peer end.
	var pilotPeer net.Conn
	for i := 0; i < 100 && pilotPeer == nil; i++ {
		d.mu.Lock()
		if len(d.peers) > 0 {
			pilotPeer = d.peers[0]
		}
		d.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	if pilotPeer == nil {
		t.Fatal("dialer never received DialAddr")
	}

	// Close pilot peer abruptly — io.Copy(tcp, pilot) returns an error,
	// then the closeBoth fall-through tears the bridge down.
	_ = pilotPeer.Close()

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("bridge did not exit after pilot peer close")
	}
}

// ---------------------------------------------------------------------------
// listenPort happy path — bind, accept once, gracefully exit on Stop
// ---------------------------------------------------------------------------

func TestListenPort_HappyPathExits(t *testing.T) {
	t.Parallel()
	d := &pipeDialerSync{}
	gw, err := New(Config{Subnet: "10.4.0.0/16", Ports: []uint16{}}, d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Probe-pick an ephemeral port.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	port := uint16(probe.Addr().(*net.TCPAddr).Port)
	probe.Close()

	exited := make(chan struct{})
	go func() {
		gw.listenPort(net.ParseIP("127.0.0.1"), port, protocol.Addr{Network: 1, Node: 1})
		close(exited)
	}()

	// Wait for listener to register, then trigger Accept by dialing.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		gw.mu.Lock()
		_, ok := gw.listeners["127.0.0.1:"+itoaCov(port)]
		gw.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+itoaCov(port), 200*time.Millisecond)
	if err == nil {
		_ = c.Close()
	}
	// Force Accept to return by closing the listener via Stop.
	gw.Stop()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("listenPort did not exit after Stop")
	}
}

// ---------------------------------------------------------------------------
// Service (the L11 plugin adapter)
// ---------------------------------------------------------------------------

func TestService_Lifecycle(t *testing.T) {
	t.Parallel()
	s := NewService()
	if s == nil {
		t.Fatal("NewService returned nil")
	}
	if got := s.Name(); got != "gateway" {
		t.Fatalf("Name: got %q, want %q", got, "gateway")
	}
	if got := s.Order(); got != 220 {
		t.Fatalf("Order: got %d, want 220", got)
	}
	if err := s.Start(context.Background(), coreapi.Deps{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Bridge dialer-Close path — exercise errDialer.Close from Stop
// ---------------------------------------------------------------------------

func TestStop_DialerCloseError(t *testing.T) {
	t.Parallel()
	d := &errDialer{closeErr: io.ErrClosedPipe}
	gw, err := New(Config{Subnet: "10.4.0.0/16"}, d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Stop ignores the dialer.Close error — must not panic.
	gw.Stop()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// withExecStubs prepends a tmpdir to PATH that shadows `ip` and `ifconfig`
// with a tiny POSIX shell script whose exit code is success (true) or
// failure (false). Restores PATH automatically via t.Cleanup. Tests using
// this MUST also guard with `os.Getuid() != 0` to avoid clobbering the
// real loopback alias table on developer machines.
func withExecStubs(t *testing.T, success bool) {
	t.Helper()
	dir := t.TempDir()
	rc := 0
	if !success {
		rc = 1
	}
	body := "#!/bin/sh\nexit " + itoaCov(uint16(rc)) + "\n"
	for _, name := range []string{"ip", "ifconfig"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatalf("write stub %s: %v", name, err)
		}
	}
	// Prepend tmpdir so the stubs shadow system binaries for this test only.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// itoaCov: tiny uint→decimal converter without strconv. Kept local so the
// other test files (which also define `itoa`) don't clash.
func itoaCov(p uint16) string {
	if p == 0 {
		return "0"
	}
	var b [6]byte
	i := len(b)
	for p > 0 {
		i--
		b[i] = byte('0' + p%10)
		p /= 10
	}
	return string(b[i:])
}
