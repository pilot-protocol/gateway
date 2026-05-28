// SPDX-License-Identifier: AGPL-3.0-or-later

package gateway

import (
	"fmt"
	"log/slog"
	"net"
	"os/exec"
)

func (gw *Gateway) addLoopbackAlias(ip net.IP) error {
	if err := exec.Command("ip", "addr", "add", ip.String()+"/32", "dev", "lo").Run(); err != nil {
		return fmt.Errorf("ip addr add %s/32 dev lo: %w", ip, err)
	}
	return nil
}

func (gw *Gateway) removeLoopbackAlias(ip net.IP) {
	if err := exec.Command("ip", "addr", "del", ip.String()+"/32", "dev", "lo").Run(); err != nil {
		slog.Error("removeLoopbackAlias failed", "ip", ip, "os", "linux", "err", err)
	}
}
