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

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

type ChrootSandbox struct{}

func (s *ChrootSandbox) Run(stdin io.Reader, stdout, stderr io.Writer, script Script, args []string) error {
	rootPath := script.Image
	if rootPath == "" {
		return fmt.Errorf("ChrootSandbox requires an image path (used as root directory)")
	}

	realRoot, _, cleanup, err := prepareRootFS(rootPath)
	if err != nil {
		return err
	}
	defer cleanup()

	// Determine the command to run
	var cmdPath string
	var cmdArgs []string

	if script.Entrypoint != "" {
		cmdPath = script.Entrypoint
		cmdArgs = append([]string{cmdPath}, args...)
	} else {
		// If no entrypoint, use the first argument as command
		if len(args) > 0 {
			cmdPath = args[0]
			cmdArgs = args
		} else {
			return fmt.Errorf("no command specified and no entrypoint in script")
		}
	}

	// Prepare the command
	cmd := execCommand(cmdPath, cmdArgs[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// Set SysProcAttr for chroot
	// We also need to set Credential/Setsid/etc?
	// Ideally we should drop privileges if we are root, but that's out of scope for now.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Chroot: realRoot,
	}

	// We start at root of the new root
	cmd.Dir = "/"

	// We are not handling mounts here yet.
	// Issue says: "leave a lot of functionality not supported"
	if len(script.Mounts) > 0 {
		return fmt.Errorf("mounts are not supported in chroot sandbox")
	}

	// The sandboxed workload must NOT inherit the host environment: start
	// from a minimal base and add only the variables explicitly declared in
	// the script's env: block.
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
	}
	if term := os.Getenv("TERM"); term != "" {
		cmd.Env = append(cmd.Env, "TERM="+term)
	}
	for _, env := range script.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", env.Name, env.Value))
	}

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("error running chroot command: %w", err)
	}

	return nil
}
