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
	"runtime"
)

var prootOperatingSystem = runtime.GOOS

type prootSandbox struct {
	executable string
}

func (s *prootSandbox) Usable(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("command cancelled: %w", err)
	}

	if prootOperatingSystem != "linux" {
		return fmt.Errorf("proot sandbox is not supported on %s", prootOperatingSystem)
	}

	executable := s.executable
	if executable == "" {
		executable = "proot"
	}

	if _, err := lookPathFunction(executable); err != nil {
		return fmt.Errorf("%s executable not found: install proot or add it to PATH", executable)
	}

	return nil
}

var prootCapabilities = Capabilities{
	Mounts:         true,
	ReadOnlyMounts: false,
	NetworkNone:    false,
}

func (s *prootSandbox) Capabilities() Capabilities {
	return prootCapabilities
}

func (s *prootSandbox) String() string {
	return "proot"
}

func (s *prootSandbox) Run(ctx context.Context, script Config, args []string, options RunOptions) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("command cancelled: %w", err)
	}

	if err := validateConfigCapabilities("proot", s.Capabilities(), script); err != nil {
		return err
	}

	if err := validateMounts(script.Mounts, ":"); err != nil {
		return err
	}

	rootPath := script.Image
	if rootPath == "" {
		return fmt.Errorf("ProotSandbox requires an image path (used as root directory)")
	}

	rootFS, err := prepareRootFSFunction(ctx, rootPath)
	if err != nil {
		return err
	}
	defer rootFS.cleanup()

	_, cmdArgs, err := resolveRootFSCommand(script.Entrypoint, args, rootFS)
	if err != nil {
		return err
	}

	// proot -r realRoot [-b host:guest ...] cmdArgs
	prootArgs := []string{"-r", rootFS.dir}
	var warnGit bool
	for _, mount := range script.Mounts {
		if mount.ReadOnly {
			warnGit = true
		}
		prootArgs = append(prootArgs, "-b", fmt.Sprintf("%s:%s", mount.HostPath, mount.SandboxPath))
	}
	if warnGit && options.Stderr != nil {
		if _, err := fmt.Fprintf(options.Stderr, "warning: proot sandbox cannot protect .git from writes\n"); err != nil {
			return err
		}
	}

	if rootFS.workingDir != "" {
		prootArgs = append(prootArgs, "-w", rootFS.workingDir)
	}

	prootArgs = append(prootArgs, cmdArgs...)

	executable := s.executable
	if executable == "" {
		executable = "proot"
	}

	// Prepare the command
	cmd := execCommand(executable, prootArgs...)
	cmd.Stdin = options.Stdin
	cmd.Stdout = options.Stdout
	cmd.Stderr = options.Stderr

	// We start at root of the new root
	cmd.Dir = "/"

	cmd.Env = buildSandboxEnv(rootFS.env, script.Env)
	// Only move to a new process group when the run is not interactive: a process
	// outside the foreground group is stopped when reading the terminal and
	// misses Ctrl-C, so interactive runs keep the caller's group (with the
	// trade-off that cancellation kills only the direct process).
	setupProcessGroup(cmd, options)

	return runCommandFunction(ctx, "proot", cmd, func() error {
		return killProcessTree(cmd)
	})
}
