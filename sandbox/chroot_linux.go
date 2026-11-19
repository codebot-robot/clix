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
	"context"
	"fmt"
	"io"
	"os"
	"syscall"
)

type chrootSandbox struct{}

func (s *chrootSandbox) Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, script Config, args []string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("command cancelled: %w", err)
	}

	if script.Network != "" && script.Network != "none" {
		return fmt.Errorf("network mode %q is not supported by the chroot sandbox", script.Network)
	}

	if len(script.Mounts) > 0 {
		return fmt.Errorf("mounts are not supported in chroot sandbox")
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
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

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
	// Only move to a new process group when stdin is not a terminal: a process
	// outside the foreground group is stopped when reading the terminal and
	// misses Ctrl-C, so interactive runs keep the caller's group (with the
	// trade-off that cancellation kills only the direct process).
	setupProcessGroup(cmd, stdin)

	return runCommandFunction(ctx, "chroot", cmd, func() error {
		return killProcessTree(cmd)
	})
}
