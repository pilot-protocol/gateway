# gateway

Pilot Protocol gateway plugin. Maps virtual pilot addresses to local
loopback aliases (`ip addr add` / `ifconfig lo0 alias`) so legacy
TCP/UDP apps can dial pilot-addressed peers as if they were on
127.x.x.x. Ports under 1024 require root on Linux.

## Layout

| File | What it does |
|---|---|
| `gateway.go` | TCP/UDP proxy that splices a local listener ↔ pilot connection. |
| `mapping.go` | Pilot-address ↔ loopback-IP allocation + alias install/remove. |
| `service.go` | `*Service` — `coreapi.Service` adapter. Build tag `!no_gateway`. |
| `service_disabled.go` | Stub when build tag `no_gateway` is set. |

## Import paths

```go
import "github.com/pilot-protocol/gateway"

g := gateway.NewService(gateway.Config{
    Dialer: driverDialer,
    Ports:  []uint16{80, 443, 8080},
})
rt.Register(g)
```

The standalone CLI entry point lives at `cmd/gateway/main.go` in the
protocol repo and constructs the dialer from `pkg/driver`.

## Disabling

Pass `-tags no_gateway` to compile a stub whose `Start` is a no-op.

## Releasing

Tag a SemVer version (e.g. `v0.1.0`); web4 pulls it in via
`require github.com/pilot-protocol/gateway v0.1.0`. During
co-development the protocol repo uses `replace ../gateway`.
