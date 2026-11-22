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
	"runtime"
	"strings"
)

var appleContainerOperatingSystem = runtime.GOOS

type appleContainerSandbox struct {
	executable string
}

func (s *appleContainerSandbox) Usable(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("command cancelled: %w", err)
	}

	if appleContainerOperatingSystem != "darwin" {
		return fmt.Errorf("apple-container requires macOS")
	}

	executable := s.executable
	if executable == "" {
		executable = "container"
	}

	if _, err := lookPathFunction(executable); err != nil {
		return fmt.Errorf("%s executable not found: install the Apple container tool", executable)
	}

	cmd := execCommand(executable, "system", "status")
	var outputBuffer bytes.Buffer
	cmd.Stdout = &outputBuffer
	cmd.Stderr = &outputBuffer

	if err := runCommandFunction(ctx, "container", cmd, nil); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return err
		}
		outputMessage := strings.TrimSpace(outputBuffer.String())
		if outputMessage != "" {
			return fmt.Errorf("apple-container service is not running: start the service with '%s system start': %s: %w", executable, outputMessage, err)
		}
		return fmt.Errorf("apple-container service is not running: start the service with '%s system start': %w", executable, err)
	}

	return nil
}

var appleContainerCapabilities = Capabilities{
	Mounts:         true,
	ReadOnlyMounts: true,
	NetworkNone:    false,
}

func (s *appleContainerSandbox) Capabilities() Capabilities {
	return appleContainerCapabilities
}

func (s *appleContainerSandbox) String() string {
	return "apple-container"
}

func (s *appleContainerSandbox) Run(ctx context.Context, script Config, args []string, options RunOptions) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("command cancelled: %w", err)
	}

	if err := validateConfigCapabilities("apple-container", s.Capabilities(), script); err != nil {
		return err
	}

	terminal := shouldAllocateTerminal(options)
	cmdArgs, err := buildAppleContainerArgs(script, args, terminal)
	if err != nil {
		return err
	}

	executable := s.executable
	if executable == "" {
		executable = "container"
	}

	cmd := execCommand(executable, cmdArgs...)
	cmd.Stdin = options.Stdin
	cmd.Stdout = options.Stdout
	cmd.Stderr = options.Stderr
	// Only move to a new process group when the run is not interactive: a process
	// outside the foreground group is stopped when reading the terminal and
	// misses Ctrl-C, so interactive runs keep the caller's group (with the
	// trade-off that cancellation kills only the direct process).
	setupProcessGroup(cmd, options)

	return runCommand(ctx, "container", cmd, func() error {
		return killProcessTree(cmd)
	})
}

func buildAppleContainerArgs(script Config, args []string, terminal bool) ([]string, error) {
	if err := validateMounts(script.Mounts, ","); err != nil {
		return nil, err
	}

	cmdArgs := []string{"run", "--rm"}
	if terminal {
		cmdArgs = append(cmdArgs, "-it")
	}

	for _, mount := range script.Mounts {
		mountArg := fmt.Sprintf("type=bind,source=%s,target=%s", mount.HostPath, mount.SandboxPath)
		if mount.ReadOnly {
			mountArg += ",readonly"
		}
		cmdArgs = append(cmdArgs, "--mount", mountArg)
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
