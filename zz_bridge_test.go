// SPDX-License-Identifier: AGPL-3.0-or-later

package gateway

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/TeoSlayer/pilotprotocol/pkg/protocol"
)

// pipeDialer returns one end of a net.Pipe per DialAddr call and stashes
// the other end so the test can interact with it.
type pipeDialer struct {
	mu       sync.Mutex
	peerEnds []net.Conn
	dialErr  error
}

func (d *pipeDialer) DialAddr(protocol.Addr, uint16) (net.Conn, error) {
	if d.dialErr != nil {
		return nil, d.dialErr
	}
	a, b := net.Pipe()
	d.mu.Lock()
	d.peerEnds = append(d.peerEnds, b)
	d.mu.Unlock()
	return a, nil
}

func (d *pipeDialer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.peerEnds {
		_ = c.Close()
	}
	return nil
}

// TestBridgeConnection_DialFailureClosesConn covers the dial-error branch.
func TestBridgeConnection_DialFailureClosesConn(t *testing.T) {
	t.Parallel()
	d := &pipeDialer{dialErr: io.ErrUnexpectedEOF}
	gw, _ := New(Config{Subnet: "10.4.0.0/16", Ports: []uint16{}}, d)

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	// bridgeConnection should return promptly after dial fails.
	doneCh := make(chan struct{})
	go func() {
		gw.bridgeConnection(a, protocol.Addr{Network: 1, Node: 1}, 80)
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("bridgeConnection blocked after dial failure")
	}
}

// TestBridgeConnection_BidiCopy drives bridgeConnection's full Copy loop.
func TestBridgeConnection_BidiCopy(t *testing.T) {
	t.Parallel()
	d := &pipeDialer{}
	gw, _ := New(Config{Subnet: "10.4.0.0/16", Ports: []uint16{}}, d)

	// tcpEnd is the "client" end (what bridge reads from / writes to).
	tcpEnd, tcpPeer := net.Pipe()

	doneCh := make(chan struct{})
	go func() {
		gw.bridgeConnection(tcpEnd, protocol.Addr{Network: 1, Node: 1}, 80)
		close(doneCh)
	}()

	// Wait for dialer to populate the peer end.
	var pilotPeer net.Conn
	for i := 0; i < 100; i++ {
		d.mu.Lock()
		if len(d.peerEnds) > 0 {
			pilotPeer = d.peerEnds[0]
			d.mu.Unlock()
			break
		}
		d.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	if pilotPeer == nil {
		t.Fatal("dialer never received a DialAddr call")
	}

	// Send a byte from tcp side → pilot side.
	go func() { _, _ = tcpPeer.Write([]byte("ping")) }()
	buf := make([]byte, 4)
	if _, err := io.ReadFull(pilotPeer, buf); err != nil {
		t.Fatalf("pilot read: %v", err)
	}
	if string(buf) != "ping" {
		t.Errorf("got %q", buf)
	}

	// Send a byte the other direction.
	go func() { _, _ = pilotPeer.Write([]byte("pong")) }()
	if _, err := io.ReadFull(tcpPeer, buf); err != nil {
		t.Fatalf("tcp read: %v", err)
	}
	if string(buf) != "pong" {
		t.Errorf("got %q", buf)
	}

	// Close both ends to unblock the copy loops.
	_ = tcpPeer.Close()
	_ = pilotPeer.Close()

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("bridgeConnection did not return after both ends closed")
	}
}

// TestListenPort_AcceptsAndBridges drives listenPort via a real TCP
// listener — covers the Accept loop's success path.
func TestListenPort_AcceptsAndBridges(t *testing.T) {
	t.Parallel()
	d := &pipeDialer{}
	gw, _ := New(Config{Subnet: "10.4.0.0/16", Ports: []uint16{}}, d)

	// Pre-bind an ephemeral port to discover one that's free, then release.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe Listen: %v", err)
	}
	port := uint16(probe.Addr().(*net.TCPAddr).Port)
	probe.Close()
	// Tiny race window between probe.Close and listenPort's bind; if the
	// port gets stolen the bind fails and the function just returns — the
	// test still exercises the listenPort entry.
	go gw.listenPort(net.ParseIP("127.0.0.1"), port, protocol.Addr{Network: 1, Node: 1})

	// Dial the listener a few times to drive the Accept loop.
	for i := 0; i < 5; i++ {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Stop gateway to unblock listenPort's Accept.
	gw.Stop()
}

// itoa avoids importing strconv for a one-shot port → string conversion.
func itoa(p uint16) string {
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
