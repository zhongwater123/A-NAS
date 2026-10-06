# Use Go for product services and the Host Agent

A-NAS will implement its initial modular product service and privileged Host Agent in Go. A shared toolchain keeps builds, deployment, contracts, and operational debugging simple while producing self-contained Linux binaries; the privilege boundary remains enforced by separate processes and narrow typed IPC rather than by the language choice. A hardware integration or local AI runtime may use another language later only behind an explicit adapter boundary.
