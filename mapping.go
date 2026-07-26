// SPDX-License-Identifier: AGPL-3.0-or-later

package gateway

import (
	"fmt"
	"net"
	"sync"

	"github.com/pilot-protocol/common/protocol"
)

// MappingTable maps local IPs to Pilot addresses and vice versa.
type MappingTable struct {
	mu      sync.RWMutex
	forward map[string]protocol.Addr // local IP → pilot addr
	reverse map[protocol.Addr]net.IP // pilot addr → local IP
	subnet  *net.IPNet
	nextIP  net.IP   // sweep cursor: addresses at or above this are untouched
	network net.IP   // subnet network address, nil when the prefix has none
	bcast   net.IP   // subnet broadcast address, nil when the prefix has none
	freed   []net.IP // released addresses, handed out before the cursor advances
	swept   bool     // cursor has reached the end of the address space
}

// NewMappingTable creates a mapping table for the given subnet (e.g. "10.4.0.0/16").
func NewMappingTable(cidr string) (*MappingTable, error) {
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("parse subnet: %w", err)
	}

	// Allocation begins one past the subnet base, which is the network
	// address for every prefix wide enough to have one.
	startIP := make(net.IP, len(subnet.IP))
	copy(startIP, subnet.IP)
	startIP[len(startIP)-1] |= 1

	network, bcast := edgeAddrs(subnet)

	return &MappingTable{
		forward: make(map[string]protocol.Addr),
		reverse: make(map[protocol.Addr]net.IP),
		subnet:  subnet,
		nextIP:  startIP,
		network: network,
		bcast:   bcast,
	}, nil
}

// edgeAddrs returns the network and broadcast addresses of subnet. Both are
// nil when the prefix leaves fewer than two addresses (/31 and /32 for IPv4,
// /127 and /128 for IPv6), where every address in the range is usable.
func edgeAddrs(subnet *net.IPNet) (network, bcast net.IP) {
	if len(subnet.Mask) != len(subnet.IP) {
		return nil, nil
	}
	ones, bits := subnet.Mask.Size()
	if bits == 0 || bits-ones < 2 {
		return nil, nil
	}

	network = make(net.IP, len(subnet.IP))
	copy(network, subnet.IP)

	bcast = make(net.IP, len(subnet.IP))
	for i := range bcast {
		bcast[i] = subnet.IP[i] | ^subnet.Mask[i]
	}
	return network, bcast
}

// reserved reports whether ip is the subnet's network or broadcast address.
func (mt *MappingTable) reserved(ip net.IP) bool {
	if mt.network != nil && mt.network.Equal(ip) {
		return true
	}
	if mt.bcast != nil && mt.bcast.Equal(ip) {
		return true
	}
	return false
}

// Map registers a mapping between a Pilot address and a local IP.
// If localIP is nil, the next available IP in the subnet is assigned.
func (mt *MappingTable) Map(pilotAddr protocol.Addr, localIP net.IP) (net.IP, error) {
	mt.mu.Lock()
	defer mt.mu.Unlock()

	// Check if already mapped
	if existing, ok := mt.reverse[pilotAddr]; ok {
		return existing, nil
	}

	if localIP == nil {
		localIP = mt.allocNextIP()
		if localIP == nil {
			return nil, fmt.Errorf("subnet exhausted")
		}
	} else {
		if !mt.subnet.Contains(localIP) {
			return nil, fmt.Errorf("IP %s not in subnet %s", localIP, mt.subnet)
		}
		if mt.reserved(localIP) {
			return nil, fmt.Errorf("IP %s is the network or broadcast address of %s", localIP, mt.subnet)
		}
	}

	ipStr := localIP.String()
	if _, exists := mt.forward[ipStr]; exists {
		return nil, fmt.Errorf("IP %s already mapped", ipStr)
	}

	mt.forward[ipStr] = pilotAddr
	mt.reverse[pilotAddr] = localIP
	return localIP, nil
}

// Unmap removes a mapping by local IP.
func (mt *MappingTable) Unmap(localIP net.IP) error {
	mt.mu.Lock()
	defer mt.mu.Unlock()

	ipStr := localIP.String()
	addr, ok := mt.forward[ipStr]
	if !ok {
		return fmt.Errorf("no mapping for %s", ipStr)
	}

	delete(mt.forward, ipStr)
	delete(mt.reverse, addr)

	// Return the address to the pool so the cursor sweep is not the only
	// source of free addresses.
	released := make(net.IP, len(localIP))
	copy(released, localIP)
	mt.freed = append(mt.freed, released)
	return nil
}

// Lookup returns the Pilot address for a local IP.
func (mt *MappingTable) Lookup(localIP net.IP) (protocol.Addr, bool) {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	addr, ok := mt.forward[localIP.String()]
	return addr, ok
}

// ReverseLookup returns the local IP for a Pilot address.
func (mt *MappingTable) ReverseLookup(addr protocol.Addr) (net.IP, bool) {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	ip, ok := mt.reverse[addr]
	return ip, ok
}

// All returns all current mappings as (localIP, pilotAddr) pairs.
type Mapping struct {
	LocalIP   net.IP
	PilotAddr protocol.Addr
}

func (mt *MappingTable) All() []Mapping {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	result := make([]Mapping, 0, len(mt.forward))
	for ipStr, addr := range mt.forward {
		result = append(result, Mapping{
			LocalIP:   net.ParseIP(ipStr),
			PilotAddr: addr,
		})
	}
	return result
}

// allocNextIP returns an unused address from the subnet, or nil when every
// usable address is taken. Released addresses are handed out first; only then
// does the cursor sweep forward over addresses never allocated before. The
// subnet's network and broadcast addresses are never returned.
func (mt *MappingTable) allocNextIP() net.IP {
	for len(mt.freed) > 0 {
		ip := mt.freed[0]
		mt.freed = mt.freed[1:]
		// An address can be re-taken by an explicit Map while it sits in
		// the pool, so re-check before handing it out.
		if _, exists := mt.forward[ip.String()]; !exists {
			return ip
		}
	}

	for !mt.swept {
		if !mt.subnet.Contains(mt.nextIP) {
			break
		}

		ip := make(net.IP, len(mt.nextIP))
		copy(ip, mt.nextIP)
		if !incIP(mt.nextIP) {
			// Cursor wrapped past the top of the address space; this is
			// the last address the sweep can offer.
			mt.swept = true
		}

		if mt.reserved(ip) {
			continue
		}
		if _, exists := mt.forward[ip.String()]; !exists {
			return ip
		}
	}
	return nil
}

// incIP increments ip in place. It reports false when the increment wrapped
// the whole address back to all-zeros.
func incIP(ip net.IP) bool {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			return true
		}
	}
	return false
}
