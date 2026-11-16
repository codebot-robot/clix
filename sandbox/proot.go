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
	"os/exec"
)

type prootSandbox struct{}

func (s *prootSandbox) Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, script Config, args []string) error {
	if script.Network != "" {
		return fmt.Errorf("network isolation is not supported by the proot sandbox")
	}

	for _, m := range script.Mounts {
		if m.ReadOnly && !m.WritableFallback {
			return fmt.Errorf("read-only mounts are not supported by the proot sandbox")
		}
	}

	rootPath := script.Image
	if rootPath == "" {
		return fmt.Errorf("ProotSandbox requires an image path (used as root directory)")
	}

	rootFS, err := prepareRootFSFn(ctx, rootPath)
	if err != nil {
		return err
	}
	defer rootFS.cleanup()

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

	// proot -r realRoot [-b host:guest ...] cmdArgs
	prootArgs := []string{"-r", rootFS.dir}
	var warnGit bool
	for _, m := range script.Mounts {
		if m.ReadOnly {
			warnGit = true
		}
		prootArgs = append(prootArgs, "-b", fmt.Sprintf("%s:%s", m.HostPath, m.SandboxPath))
	}
	if warnGit {
		if _, err := fmt.Fprintf(stderr, "warning: proot sandbox cannot protect .git from writes\n"); err != nil {
			return err
		}
	}

	prootArgs = append(prootArgs, cmdArgs...)

	// Prepare the command
	cmd := execCommand("proot", prootArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// We start at root of the new root
	cmd.Dir = "/"

	cmd.Env = buildSandboxEnv(rootFS.env, script.Env)

	if err := cmd.Run(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return newExitError(exitError.ExitCode())
		}
		return fmt.Errorf("error running proot command: %w", err)
	}

	return nil
}
