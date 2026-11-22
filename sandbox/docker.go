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
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/term"
)

var dockerStopTimeout = 10 * time.Second

type dockerSandbox struct {
	executable string
}

func (s *dockerSandbox) Usable(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("command cancelled: %w", err)
	}

	executable := s.executable
	if executable == "" {
		executable = "docker"
	}

	if _, err := lookPathFunction(executable); err != nil {
		return fmt.Errorf("%s executable not found: install Docker or add it to PATH", executable)
	}

	cmd := execCommand(executable, "info")
	var outputBuffer bytes.Buffer
	cmd.Stdout = &outputBuffer
	cmd.Stderr = &outputBuffer

	if err := runCommandFunction(ctx, "docker", cmd, nil); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return err
		}
		outputMessage := strings.TrimSpace(outputBuffer.String())
		if outputMessage != "" {
			return fmt.Errorf("docker engine is not responding: start the Docker service or verify permissions: %s: %w", outputMessage, err)
		}
		return fmt.Errorf("docker engine is not responding: start the Docker service or verify permissions: %w", err)
	}

	return nil
}

var dockerCapabilities = Capabilities{
	Mounts:         true,
	ReadOnlyMounts: true,
	NetworkNone:    true,
}

func (s *dockerSandbox) Capabilities() Capabilities {
	return dockerCapabilities
}

func (s *dockerSandbox) String() string {
	return "docker"
}

func (s *dockerSandbox) Run(ctx context.Context, script Config, args []string, options RunOptions) error {
	logger := logr.FromContextOrDiscard(ctx)
	logger.V(2).Info("DockerSandbox: preparing args")
	containerName, err := generateContainerNameFunction()
	if err != nil {
		return err
	}
	cmdArgs, err := buildDockerArgs(script, args, shouldAllocateTerminal(options))
	if err != nil {
		return err
	}
	cmdArgs = append([]string{"run", "--name", containerName}, cmdArgs[1:]...)

	executable := s.executable
	if executable == "" {
		executable = "docker"
	}

	logger.V(1).Info(fmt.Sprintf("DockerSandbox: running %s %v", executable, cmdArgs))
	cmd := execCommand(executable, cmdArgs...)
	cmd.Stdin = options.Stdin
	cmd.Stdout = options.Stdout
	cmd.Stderr = options.Stderr

	stopAction := func() error {
		var killError error
		if cmd.Process != nil {
			if processKillError := cmd.Process.Kill(); processKillError != nil && !errors.Is(processKillError, os.ErrProcessDone) && !errors.Is(processKillError, syscall.ESRCH) {
				killError = processKillError
			}
		}
		stopError := stopDockerContainer(executable, containerName)
		return errors.Join(killError, stopError)
	}

	return runCommand(ctx, "docker", cmd, stopAction)
}

func stopDockerContainer(executable string, containerName string) error {
	stopCommand := execCommand(executable, "rm", "-f", containerName)
	var output bytes.Buffer
	stopCommand.Stdout = &output
	stopCommand.Stderr = &output
	if err := stopCommand.Start(); err != nil {
		return fmt.Errorf("failed to stop container %s (remove with '%s rm -f %s'): %w", containerName, executable, containerName, err)
	}

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- stopCommand.Wait()
	}()

	select {
	case err := <-stopDone:
		if err != nil && !strings.Contains(output.String(), "No such container") && !strings.Contains(output.String(), "is already in progress") {
			return fmt.Errorf("failed to stop container %s (remove with '%s rm -f %s'): %w", containerName, executable, containerName, err)
		}
		return nil
	case <-time.After(dockerStopTimeout):
		var killError error
		if stopCommand.Process != nil {
			if processKillError := stopCommand.Process.Kill(); processKillError != nil && !errors.Is(processKillError, os.ErrProcessDone) && !errors.Is(processKillError, syscall.ESRCH) {
				killError = processKillError
			}
		}
		<-stopDone
		timeoutError := fmt.Errorf("failed to stop container %s within %v (remove with '%s rm -f %s')", containerName, dockerStopTimeout, executable, containerName)
		return errors.Join(timeoutError, killError)
	}
}

func buildDockerArgs(script Config, args []string, terminal bool) ([]string, error) {
	if err := validateConfigCapabilities("docker", dockerCapabilities, script); err != nil {
		return nil, err
	}
	if err := validateMounts(script.Mounts, ":"); err != nil {
		return nil, err
	}

	cmdArgs := []string{"run", "--rm", "-i"}
	if terminal {
		cmdArgs = append(cmdArgs, "-t")
	}

	if script.Network == "none" {
		cmdArgs = append(cmdArgs, "--network", "none")
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

var isTerminalFunction = func(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

func isTerminal(reader io.Reader) bool {
	return isTerminalFunction(reader)
}

func isInteractive(options RunOptions) bool {
	if options.Interactive != nil {
		return *options.Interactive
	}
	return isTerminal(options.Stdin)
}

func shouldAllocateTerminal(options RunOptions) bool {
	return isInteractive(options) && isTerminal(options.Stdin)
}
