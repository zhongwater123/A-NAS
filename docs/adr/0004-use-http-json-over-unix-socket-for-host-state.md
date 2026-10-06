# Use HTTP and JSON over a Unix socket for read-only Host Agent state

The Product Service will consume read-only Host Agent state through HTTP/JSON on a Unix Domain Socket. The Host Agent exposes one atomic `GET /v1/state` operation; a client Adapter hides transport, validation, timeout, and DTO mapping behind the existing `hoststate.Reader` seam. This preserves one point-in-time observation and keeps the Product Service from loading the Linux Adapter directly.

The socket defaults to `$XDG_RUNTIME_DIR/a-nas/host-agent.sock`, is mode `0600`, and is not reachable over TCP. The current Host Agent remains unprivileged and read-only. This decision does not authorize future disk mutations or prescribe the protocol for execution plans, event streaming, or a later privileged Host Agent.
