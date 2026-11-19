# Sandboxing Design

This document describes the design for running tools in a sandbox environment (Docker) using `clix`.

## Goals

*   Allow tools to run in isolated Docker containers.
*   Support mounting host directories into the container.
*   Provide a way to dynamically determine mount points (e.g., git repository root).

## Sandbox contract

*   **Environment.** A sandboxed tool receives only the variables listed in the script's `env:` block, plus whatever the container image itself defines, plus a minimal base that a backend must supply for the tool to run at all (`PATH`, `HOME`, and `TERM` when attached to a terminal). The host environment is never inherited or passed through, on any backend. There is no syntax for "pass this host variable through"; a value in `env:` is a literal.
*   **Filesystem.** A sandboxed tool sees the image's filesystem plus only the host directories listed in `mounts:`.
*   **Network.** By default, a sandboxed tool keeps the network access of the host environment. A run can declare `network: none` to request that no network access be available. A backend that cannot honour this must fail with an error naming the backend and the reason. The `docker` and `chroot` backends enforce `network: none`; `apple-container` and `proot` reject runs that request it.
*   **Backends.** The contract is the same for every backend selected by `CLIX_SANDBOX` (`docker`, the default; `apple-container`; `chroot`; `proot`). A backend that cannot honour part of the contract must fail with an error rather than run with less isolation.
*   **Not sandboxed today.** A script with `go:` and no `mounts:` is run with `go run` directly on the host (`runGo` in `main.go`), with the host environment and filesystem. This is the current behaviour of the prototype.

## Configuration

We will add `mounts` and `env` sections to the script configuration.

```yaml
mounts:
  - hostPath: <expression>
    sandboxPath: <path> # Optional, defaults to host path
    readOnly: <boolean> # Optional, defaults to false (not implemented yet)
env:
  - name: <string>
    value: <string>
network: <string> # Optional, "none" for no network access
```

Example:

```yaml
env:
  - name: PYTHONPYCACHEPREFIX
    value: /root/.cache/python
```

### Host Expressions

*   `git.repoRoot(cwd)`: Resolves to the root of the git repository containing the current working directory.
*   (Future) Literal paths.
*   (Future) Other dynamic expressions.

## Execution Model

When `mounts` are specified (or if sandboxing is explicitly enabled), `clix` will:

1.  Resolve the mount points.
2.  Construct a Docker command.
    *   For `go` scripts, use the `golang:latest` image.
    *   Mount the requested volumes.
    *   Set the working directory inside the container to match the current working directory (which should be inside one of the mounts).
    *   Pass environment variables per the [Sandbox contract](#sandbox-contract).
3.  Execute the command inside the container.

## Go Tools

For tools defined with `go:`, we will use the `golang` official image.

```yaml
go:
  run: github.com/example/tool
mounts:
- hostPath: git.repoRoot(cwd)
```

This will result in:

```bash
docker run \
  -v /path/to/repo:/path/to/repo \
  -w /current/work/dir \
  golang:latest \
  go run github.com/example/tool
```

## Library Usage

The sandboxing can be imported as a library (`github.com/gke-labs/clix/sandbox`). A caller can select a backend by name, from `CLIX_SANDBOX`, or pick the first usable backend on the host via `sandbox.FirstUsable(ctx, options...)`. Specify executable paths via `sandbox.WithExecutable(backend, executable)`, configure mounts and environment variables, pass a logger via `context.Context`, wire I/O streams, and receive tool execution failures as `*sandbox.ExitError`.

### Example

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/gke-labs/clix/sandbox"
)

func main() {
	ctx := context.Background()
	backend, err := sandbox.FirstUsable(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to select sandbox: %v\n", err)
		os.Exit(1)
	}

	config := sandbox.Config{
		Image: "alpine:latest",
		Mounts: []sandbox.Mount{
			{
				HostPath:    "/path/to/host/dir",
				SandboxPath: "/workspace",
				ReadOnly:    true,
			},
		},
		Env: []sandbox.EnvVar{
			{
				Name:  "FOO",
				Value: "bar",
			},
		},
	}

	err = backend.Run(ctx, os.Stdin, os.Stdout, os.Stderr, config, []string{"echo", "hello"})
	if err != nil {
		var exitError *sandbox.ExitError
		if errors.As(err, &exitError) {
			os.Exit(exitError.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "sandbox run failed: %v\n", err)
		os.Exit(1)
	}
}
```

### Usability Probing and Priority Order

`sandbox.FirstUsable(ctx, options...)` evaluates backends in a fixed, documented priority order:
1. **`docker`**: Checks that the Docker executable is found and that the Docker engine responds to `docker info`.
2. **`apple-container`**: Checks that the host is macOS (`darwin`), that the `container` executable is found, and that the container service responds to `container system status`.
3. **`chroot`**: Checks that the host is Linux and that unprivileged user namespaces are available for `chroot`.
4. **`proot`**: Checks that the host is Linux and that `proot` is installed.

Each backend reports whether it is usable on the host via `backend.Usable(ctx)`. When not usable, it returns an actionable, single-sentence reason. If no backend is usable on the host, `FirstUsable` returns an error listing all candidate backends tried and why each was not usable. Individual backends can also be checked by name using `sandbox.CheckUsable(ctx, backendName, options...)`.

### Platform Limits

*   **`docker`**: Requires a running Docker daemon. Supported on Linux, macOS, and other platforms where Docker is available. Supports `network: none`.
*   **`apple-container`**: Requires macOS with the `container` CLI installed. Does not support network isolation (`network: none`).
*   **`chroot`**: Supported only on Linux; fails on other platforms. Requires root privileges or unprivileged user namespace support. Does not support mounts. Supports `network: none`.
*   **`proot`**: Requires `proot` installed in `$PATH`. Supported on Linux. Does not support read-only mounts unless fallback is allowed, and does not support network isolation (`network: none`).
