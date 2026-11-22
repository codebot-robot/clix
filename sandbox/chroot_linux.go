// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// isUserNamespaceError reports whether err indicates that user namespaces are
// unavailable or not permitted on this kernel/host (see clone(2) and user_namespaces(7)):
//   - EPERM: unprivileged user namespaces disabled (e.g. kernel.unprivileged_userns_clone = 0),
//     missing capabilities, or caller is in a chroot.
//   - EINVAL: kernel was compiled without user namespace support (CONFIG_USER_NS disabled).
//   - ENOSPC: user namespace limit reached or disabled via user.max_user_namespaces = 0 (Linux >= 3.11).
//   - EUSERS: older kernel limit reached for nested user namespaces (Linux 3.11 to 4.8).
func isUserNamespaceError(err error) bool {
	return errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOSPC) ||
		errors.Is(err, syscall.EUSERS)
}

func checkUserNamespaces(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("command cancelled: %w", err)
	}

	truePath, err := lookPathFunction("true")
	if err != nil {
		if _, statErr := os.Stat("/bin/true"); statErr == nil {
			truePath = "/bin/true"
		} else if _, statErr := os.Stat("/usr/bin/true"); statErr == nil {
			truePath = "/usr/bin/true"
		} else {
			return fmt.Errorf("cannot probe user namespaces: true executable not found on PATH: %w", err)
		}
	}

	cmd := execCommand(truePath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER,
	}
	var outputBuffer bytes.Buffer
	cmd.Stdout = &outputBuffer
	cmd.Stderr = &outputBuffer

	if err := runCommandFunction(ctx, "chroot", cmd, nil); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return err
		}
		if isUserNamespaceError(err) {
			return fmt.Errorf("unprivileged user namespaces are not available for chroot: enable kernel user namespaces: %w", err)
		}
		outputMessage := strings.TrimSpace(outputBuffer.String())
		if outputMessage != "" {
			return fmt.Errorf("cannot probe user namespaces: %s: %w", outputMessage, err)
		}
		return fmt.Errorf("cannot probe user namespaces: %w", err)
	}

	return nil
}

var checkUserNamespacesFunction = checkUserNamespaces

func (s *chrootSandbox) Usable(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("command cancelled: %w", err)
	}

	return checkUserNamespacesFunction(ctx)
}

func (s *chrootSandbox) Run(ctx context.Context, script Config, args []string, options RunOptions) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("command cancelled: %w", err)
	}

	if err := validateConfigCapabilities("chroot", s.Capabilities(), script); err != nil {
		return err
	}

	rootPath := script.Image
	if rootPath == "" {
		return fmt.Errorf("ChrootSandbox requires an image path (used as root directory)")
	}

	rootFS, err := prepareRootFSFunction(ctx, rootPath)
	if err != nil {
		return err
	}
	defer rootFS.cleanup()

	cmdPath, cmdArgs, err := resolveRootFSCommand(script.Entrypoint, args, rootFS)
	if err != nil {
		return err
	}

	// Prepare the command
	cmd := execCommand(cmdPath, cmdArgs[1:]...)
	cmd.Stdin = options.Stdin
	cmd.Stdout = options.Stdout
	cmd.Stderr = options.Stderr

	// Set SysProcAttr for chroot.
	// The workload must not inherit our euid 0 / CAP_SYS_CHROOT or it can
	// trivially double-chroot back out to the host root. Put it in its own
	// user + mount namespace mapped to a non-root in-namespace uid: the
	// pre-exec child still holds CAP_SYS_CHROOT in the new ns (so the
	// Chroot below succeeds), but execve() with a non-zero euid drops all
	// capabilities, so the workload itself cannot chroot(2) again.
	const sandboxID = 65534
	cloneflags := uintptr(syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWIPC)
	if script.Network == "none" {
		cloneflags |= syscall.CLONE_NEWNET
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Chroot:     rootFS.dir,
		Cloneflags: cloneflags,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: sandboxID, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: sandboxID, HostID: os.Getgid(), Size: 1},
		},
		GidMappingsEnableSetgroups: false,
	}

	// We start at root of the new root, or the image's configured working directory.
	cmd.Dir = "/"
	if rootFS.workingDir != "" {
		cmd.Dir = rootFS.workingDir
	}

	cmd.Env = buildSandboxEnv(rootFS.env, script.Env)
	// Only move to a new process group when the run is not interactive: a process
	// outside the foreground group is stopped when reading the terminal and
	// misses Ctrl-C, so interactive runs keep the caller's group (with the
	// trade-off that cancellation kills only the direct process).
	setupProcessGroup(cmd, options)

	return runCommandFunction(ctx, "chroot", cmd, func() error {
		return killProcessTree(cmd)
	})
}
