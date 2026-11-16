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
	"os/exec"
)

type appleContainerSandbox struct{}

func (s *appleContainerSandbox) Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, script Config, args []string) error {
	if script.Network != "" {
		return fmt.Errorf("network isolation is not supported by the apple-container sandbox")
	}

	cmdArgs, err := buildAppleContainerArgs(script, args, isTerminal(stdin))
	if err != nil {
		return fmt.Errorf("error building apple/container args: %w", err)
	}

	cmd := execCommand("container", cmdArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return newExitError(exitError.ExitCode())
		}
		return fmt.Errorf("error running container command: %w", err)
	}
	return nil
}

func buildAppleContainerArgs(script Config, args []string, isTerm bool) ([]string, error) {
	cmdArgs := []string{"run", "--rm"}
	if isTerm {
		cmdArgs = append(cmdArgs, "-it")
	}

	for _, m := range script.Mounts {
		mountArg := fmt.Sprintf("type=bind,source=%s,target=%s", m.HostPath, m.SandboxPath)
		if m.ReadOnly {
			mountArg += ",readonly"
		}
		cmdArgs = append(cmdArgs, "--mount", mountArg)
	}

	for _, e := range script.Env {
		cmdArgs = append(cmdArgs, "-e", fmt.Sprintf("%s=%s", e.Name, e.Value))
	}

	// Set working directory to CWD if possible
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("error getting current working directory: %w", err)
	}
	cmdArgs = append(cmdArgs, "-w", cwd)

	if script.Entrypoint != "" {
		cmdArgs = append(cmdArgs, "--entrypoint", script.Entrypoint)
	}
	cmdArgs = append(cmdArgs, script.Image)
	cmdArgs = append(cmdArgs, args...)

	return cmdArgs, nil
}
