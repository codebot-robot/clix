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
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// DockerSandbox executes container workloads using the local Docker daemon.
type DockerSandbox struct{}

// Run executes the container image described by config using Docker.
func (s *DockerSandbox) Run(stdin io.Reader, stdout, stderr io.Writer, config Config, args []string) error {
	log(2, "DockerSandbox: preparing args")
	cmdArgs, err := buildDockerArgs(config, args, isTerminal(stdin))
	if err != nil {
		return fmt.Errorf("error building docker args: %w", err)
	}

	log(1, "DockerSandbox: running docker %v", cmdArgs)
	cmd := ExecCommand("docker", cmdArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error running docker command: %w", err)
	}
	return nil
}

func buildDockerArgs(config Config, args []string, isTerm bool) ([]string, error) {
	cmdArgs := []string{"run", "--rm", "-i"}
	if isTerm {
		cmdArgs = append(cmdArgs, "-t")
	}

	for _, m := range config.Mounts {
		validMount, err := validateMount(m)
		if err != nil {
			return nil, err
		}
		mountArg := fmt.Sprintf("%s:%s", validMount.HostPath, validMount.SandboxPath)
		if validMount.ReadOnly {
			mountArg += ":ro"
		}
		cmdArgs = append(cmdArgs, "-v", mountArg)
	}

	for _, e := range config.Env {
		cmdArgs = append(cmdArgs, "-e", fmt.Sprintf("%s=%s", e.Name, e.Value))
	}

	workDir := config.WorkingDir
	if workDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("error getting current working directory: %w", err)
		}
		workDir = cwd
	}
	cmdArgs = append(cmdArgs, "-w", workDir)

	if config.Entrypoint != "" {
		cmdArgs = append(cmdArgs, "--entrypoint", config.Entrypoint)
	}
	cmdArgs = append(cmdArgs, config.Image)
	cmdArgs = append(cmdArgs, args...)

	return cmdArgs, nil
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
