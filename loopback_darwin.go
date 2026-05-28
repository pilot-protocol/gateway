// SPDX-License-Identifier: AGPL-3.0-or-later

package gateway

import (
	"log/slog"
	"net"
	"os/exec"
)

func (gw *Gateway) addLoopbackAlias(ip net.IP) {
	if err := exec.Command("ifconfig", "lo0", "alias", ip.String()).Run(); err != nil {
		slog.Error("addLoopbackAlias failed", "ip", ip, "os", "darwin", "err", err)
	}
}

func (gw *Gateway) removeLoopbackAlias(ip net.IP) {
	if err := exec.Command("ifconfig", "lo0", "-alias", ip.String()).Run(); err != nil {
		slog.Error("removeLoopbackAlias failed", "ip", ip, "os", "darwin", "err", err)
	}
}
