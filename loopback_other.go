// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux && !darwin

package gateway

import (
	"fmt"
	"log/slog"
	"net"
	"runtime"
)

func (gw *Gateway) addLoopbackAlias(ip net.IP) error {
	return fmt.Errorf("loopback alias unsupported on %s", runtime.GOOS)
}

func (gw *Gateway) removeLoopbackAlias(ip net.IP) {
	slog.Error("removeLoopbackAlias: unsupported OS", "ip", ip, "os", runtime.GOOS)
}
