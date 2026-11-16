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

	"github.com/go-logr/logr"
	"golang.org/x/term"
)

type dockerSandbox struct{}

func (s *dockerSandbox) Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, script Config, args []string) error {
	logger := logr.FromContextOrDiscard(ctx)
	logger.V(2).Info("DockerSandbox: preparing args")
	cmdArgs, err := buildDockerArgs(script, args, isTerminal(stdin))
	if err != nil {
		return err
	}

	logger.V(1).Info(fmt.Sprintf("DockerSandbox: running docker %v", cmdArgs))
	cmd := execCommand("docker", cmdArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return newExitError(exitError.ExitCode())
		}
		return fmt.Errorf("error running docker command: %w", err)
	}
	return nil
}

func buildDockerArgs(script Config, args []string, terminal bool) ([]string, error) {
	if err := validateMounts(script.Mounts, ":"); err != nil {
		return nil, err
	}

	cmdArgs := []string{"run", "--rm", "-i"}
	if terminal {
		cmdArgs = append(cmdArgs, "-t")
	}

	if script.Network == "none" {
		cmdArgs = append(cmdArgs, "--network", "none")
	} else if script.Network != "" {
		return nil, fmt.Errorf("network mode %q is not supported by the docker sandbox", script.Network)
	}

	for _, mount := range script.Mounts {
		mountArg := fmt.Sprintf("%s:%s", mount.HostPath, mount.SandboxPath)
		if mount.ReadOnly {
			mountArg += ":ro"
		}
		cmdArgs = append(cmdArgs, "-v", mountArg)
	}

	for _, envVar := range script.Env {
		cmdArgs = append(cmdArgs, "-e", fmt.Sprintf("%s=%s", envVar.Name, envVar.Value))
	}

	// Set working directory to CWD if possible
	workingDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("error getting current working directory: %w", err)
	}
	cmdArgs = append(cmdArgs, "-w", workingDir)

	if script.Entrypoint != "" {
		cmdArgs = append(cmdArgs, "--entrypoint", script.Entrypoint)
	}
	cmdArgs = append(cmdArgs, script.Image)
	cmdArgs = append(cmdArgs, args...)

	return cmdArgs, nil
}

func isTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}
