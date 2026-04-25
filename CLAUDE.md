# Claude Code Instructions

## Go

- **Never use `go build`** — do not compile Go code into binaries under any circumstances
- Always run Go programs with `go run <path>`
- Use `go vet <path>` for static analysis
- Use `go fix <path>` for automated fixes
- Use `go test <path>` for tests

This keeps the repo free of committed binaries and ensures all Go is run from source.
