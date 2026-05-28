// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux && !darwin

package gateway

import (
	"log/slog"
	"net"
	"runtime"
)

func (gw *Gateway) addLoopbackAlias(ip net.IP) {
	slog.Error("addLoopbackAlias: unsupported OS", "ip", ip, "os", runtime.GOOS)
}

func (gw *Gateway) removeLoopbackAlias(ip net.IP) {
	slog.Error("removeLoopbackAlias: unsupported OS", "ip", ip, "os", runtime.GOOS)
}
