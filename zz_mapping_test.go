// SPDX-License-Identifier: AGPL-3.0-or-later

package gateway

import (
	"net"
	"strings"
	"testing"

	"github.com/TeoSlayer/pilotprotocol/pkg/protocol"
)

func TestNewMappingTable_InvalidCIDR(t *testing.T) {
	t.Parallel()
	if _, err := NewMappingTable("not-a-cidr"); err == nil {
		t.Fatal("expected error on bad CIDR")
	}
}

func TestMappingTable_AutoAssignAndLookup(t *testing.T) {
	t.Parallel()
	mt, err := NewMappingTable("10.4.0.0/16")
	if err != nil {
		t.Fatalf("NewMappingTable: %v", err)
	}
	addr := protocol.Addr{Network: 1, Node: 0x42}
	ip, err := mt.Map(addr, nil)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if ip == nil || !strings.HasPrefix(ip.String(), "10.4.") {
		t.Errorf("got IP %v, want 10.4.x.x", ip)
	}

	// Forward lookup.
	gotAddr, ok := mt.Lookup(ip)
	if !ok || gotAddr != addr {
		t.Errorf("Lookup = (%v, %v); want %v, true", gotAddr, ok, addr)
	}
	// Reverse lookup.
	gotIP, ok := mt.ReverseLookup(addr)
	if !ok || !gotIP.Equal(ip) {
		t.Errorf("ReverseLookup = (%v, %v); want %v, true", gotIP, ok, ip)
	}
	// All() returns one entry.
	all := mt.All()
	if len(all) != 1 || all[0].PilotAddr != addr {
		t.Errorf("All = %+v, want one entry for %v", all, addr)
	}

	// Mapping the same pilot addr returns the same IP (idempotent).
	ip2, err := mt.Map(addr, nil)
	if err != nil {
		t.Fatalf("Map idempotent: %v", err)
	}
	if !ip2.Equal(ip) {
		t.Errorf("idempotent Map: got %v, want %v", ip2, ip)
	}
}

func TestMappingTable_ExplicitIPOutOfSubnet(t *testing.T) {
	t.Parallel()
	mt, _ := NewMappingTable("10.4.0.0/16")
	addr := protocol.Addr{Network: 1, Node: 1}
	if _, err := mt.Map(addr, net.ParseIP("172.16.0.1")); err == nil {
		t.Error("expected error mapping IP outside subnet")
	}
}

func TestMappingTable_ExplicitIPCollision(t *testing.T) {
	t.Parallel()
	mt, _ := NewMappingTable("10.4.0.0/16")
	first := net.ParseIP("10.4.0.99")
	a1 := protocol.Addr{Network: 1, Node: 1}
	a2 := protocol.Addr{Network: 1, Node: 2}

	if _, err := mt.Map(a1, first); err != nil {
		t.Fatalf("first Map: %v", err)
	}
	if _, err := mt.Map(a2, first); err == nil {
		t.Error("expected error mapping second pilot addr to occupied IP")
	}
}

func TestMappingTable_UnmapHappyAndAbsent(t *testing.T) {
	t.Parallel()
	mt, _ := NewMappingTable("10.4.0.0/16")
	addr := protocol.Addr{Network: 1, Node: 1}
	ip, _ := mt.Map(addr, nil)

	if err := mt.Unmap(ip); err != nil {
		t.Errorf("Unmap: %v", err)
	}
	// Already-unmapped should error.
	if err := mt.Unmap(ip); err == nil {
		t.Error("expected error unmapping absent IP")
	}
	// After unmap, lookups should miss.
	if _, ok := mt.Lookup(ip); ok {
		t.Error("Lookup should miss after Unmap")
	}
	if _, ok := mt.ReverseLookup(addr); ok {
		t.Error("ReverseLookup should miss after Unmap")
	}
}

func TestMappingTable_LookupMisses(t *testing.T) {
	t.Parallel()
	mt, _ := NewMappingTable("10.4.0.0/16")
	if _, ok := mt.Lookup(net.ParseIP("10.4.99.99")); ok {
		t.Error("Lookup of unmapped IP should return false")
	}
	if _, ok := mt.ReverseLookup(protocol.Addr{Network: 1, Node: 0x9999}); ok {
		t.Error("ReverseLookup of unmapped addr should return false")
	}
	if all := mt.All(); len(all) != 0 {
		t.Errorf("All() on empty: len = %d, want 0", len(all))
	}
}

// TestIncIP tests the carry path inside the package.
func TestIncIP(t *testing.T) {
	t.Parallel()
	ip := net.IP{10, 0, 0, 255}
	incIP(ip)
	if ip[2] != 1 || ip[3] != 0 {
		t.Errorf("after inc of 10.0.0.255: %v, want 10.0.1.0", ip)
	}
	ip = net.IP{10, 0, 255, 255}
	incIP(ip)
	if ip[1] != 1 || ip[2] != 0 || ip[3] != 0 {
		t.Errorf("after inc of 10.0.255.255: %v, want 10.1.0.0", ip)
	}
}

// TestAllocNextIP_SubnetExhaustion uses a tiny /30 subnet to drive the
// allocNextIP loop until it returns nil.
func TestAllocNextIP_SubnetExhaustion(t *testing.T) {
	t.Parallel()
	mt, err := NewMappingTable("10.4.0.0/30")
	if err != nil {
		t.Fatalf("NewMappingTable: %v", err)
	}
	// /30 has 4 IPs (10.4.0.0..3). Map until exhausted.
	var lastErr error
	mapped := 0
	for i := 0; i < 10; i++ {
		addr := protocol.Addr{Network: 1, Node: uint32(i) + 1}
		if _, err := mt.Map(addr, nil); err != nil {
			lastErr = err
			break
		}
		mapped++
	}
	if lastErr == nil {
		t.Errorf("expected exhaustion error after %d maps", mapped)
	}
	if !strings.Contains(lastErr.Error(), "exhausted") {
		t.Errorf("error = %v, want 'exhausted'", lastErr)
	}
}
