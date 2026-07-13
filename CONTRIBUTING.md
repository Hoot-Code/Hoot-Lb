# Contributing to Hoot-Lb

Thank you for considering contributing to Hoot-Lb. This document
explains how to get started.

## Getting started

1. Fork the repository and clone your fork.
2. Make sure Go 1.22+ is installed.
3. Run the full test suite to confirm everything passes:

```sh
go build ./...
go vet ./...
gofmt -l .   # must produce zero output
go test -race ./...
```

## Development guidelines

- **No external dependencies.** The entire project uses only the Go
  standard library. Do not add any `require` lines to `go.mod`.
- **Keep files under 400 lines.** Split large files into focused
  units rather than letting them grow unbounded.
- **Run `gofmt`** on all new code. The project uses `gofmt -l .` in
  CI and expects zero output.
- **Tests must pass with `-race`.** Every package must be free of
  data races. Run `go test -race ./...` before submitting.
- **Consul build tag.** Any code gated behind the `consul` build tag
  must also compile and pass tests without the tag. CI runs both paths.

## Pull requests

1. Create a feature branch from `main`.
2. Make your changes, adding tests for new functionality.
3. Ensure `go build`, `go vet`, `gofmt -l .`, and `go test -race ./...`
   all pass.
4. Open a pull request against `main` with a clear description of what
   changed and why.

## Reporting issues

Open a GitHub issue with:

- Steps to reproduce
- Expected behavior
- Actual behavior
- Go version and OS

## Code of conduct

This project follows the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md).
