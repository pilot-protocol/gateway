# gateway

[![ci](https://github.com/pilot-protocol/gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/pilot-protocol/gateway/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/pilot-protocol/gateway/branch/main/graph/badge.svg)](https://codecov.io/gh/pilot-protocol/gateway)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)

Gateway plugin for the Pilot Protocol daemon. Maps virtual pilot
addresses to local loopback aliases (`ip addr add` on Linux,
`ifconfig lo0 alias` on macOS) so legacy TCP/UDP apps can dial
pilot-addressed peers as if they were on 127.x.x.x. Ports under 1024
require root on Linux.

## Install

```go
import "github.com/pilot-protocol/gateway"
```

## Usage

```go
g := gateway.NewService(gateway.Config{
    Dialer: driverDialer,
    Ports:  []uint16{80, 443, 8080},
})
rt.Register(g)
```

## Layout

| File | What it does |
|---|---|
| `gateway.go` | TCP/UDP proxy that splices a local listener with a pilot connection. |
| `mapping.go` | Pilot-address to loopback-IP allocation, plus alias install/remove. |
| `service.go` | `*Service` — `coreapi.Service` adapter. Build tag `!no_gateway`. |
| `service_disabled.go` | Stub when build tag `no_gateway` is set. |

## Build tags

| Tag | Effect |
|---|---|
| `no_gateway` | Compiles a stub whose `Start` is a no-op. |

## License

AGPL-3.0-or-later. See [LICENSE](LICENSE).
