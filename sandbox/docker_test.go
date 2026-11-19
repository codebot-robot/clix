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
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBuildDockerArgs(t *testing.T) {
	// 1. Basic case
	script := Config{
		Image: "python:3.11",
		Env: []EnvVar{
			{Name: "VAR_A", Value: "VAL_A"},
		},
	}
	args := []string{"script.py"}
	cmdArgs, err := buildDockerArgs(script, args, false)
	if err != nil {
		t.Fatalf("buildDockerArgs failed: %v", err)
	}
	// Check basics
	// cmdArgs: [run --rm -i -w ... image args...]
	if len(cmdArgs) < 3 || cmdArgs[0] != "run" || cmdArgs[1] != "--rm" || cmdArgs[2] != "-i" {
		t.Errorf("Expected cmdArgs to start with [run, --rm, -i], got %v", cmdArgs)
	}
	foundRun := false
	foundRm := false
	foundImage := false
	foundEnv := false
	for i, arg := range cmdArgs {
		if arg == "run" {
			foundRun = true
		}
		if arg == "--rm" {
			foundRm = true
		}
		if arg == "python:3.11" {
			foundImage = true
		}
		if arg == "-e" && i+1 < len(cmdArgs) && cmdArgs[i+1] == "VAR_A=VAL_A" {
			foundEnv = true
		}
	}
	if !foundRun {
		t.Errorf("Expected 'run' in args, got %v", cmdArgs)
	}
	if !foundRm {
		t.Errorf("Expected '--rm' in args, got %v", cmdArgs)
	}
	if !foundImage {
		t.Errorf("Expected image python:3.11 in args, got %v", cmdArgs)
	}
	if !foundEnv {
		t.Errorf("Expected environment variable VAR_A=VAL_A in args, got %v", cmdArgs)
	}

	termArgs, err := buildDockerArgs(script, args, true)
	if err != nil {
		t.Fatalf("buildDockerArgs with isTerm=true failed: %v", err)
	}
	if len(termArgs) < 4 || termArgs[0] != "run" || termArgs[1] != "--rm" || termArgs[2] != "-i" || termArgs[3] != "-t" {
		t.Errorf("Expected termArgs to start with [run, --rm, -i, -t], got %v", termArgs)
	}

	// 2. Mount read-only table cases
	mountTests := []struct {
		name         string
		mounts       []Mount
		expectedArgs []string
	}{
		{
			name: "Read-only mount",
			mounts: []Mount{
				{HostPath: "/host/path", SandboxPath: "/sandbox/path", ReadOnly: true},
			},
			expectedArgs: []string{"-v", "/host/path:/sandbox/path:ro"},
		},
		{
			name: "Read-write mount",
			mounts: []Mount{
				{HostPath: "/host/path", SandboxPath: "/sandbox/path", ReadOnly: false},
			},
			expectedArgs: []string{"-v", "/host/path:/sandbox/path"},
		},
	}

	for _, tt := range mountTests {
		t.Run(tt.name, func(t *testing.T) {
			s := Config{
				Image:  "python:3.11",
				Mounts: tt.mounts,
			}
			cmdArgs, err := buildDockerArgs(s, args, false)
			if err != nil {
				t.Fatalf("buildDockerArgs failed: %v", err)
			}
			found := false
			for i := 0; i < len(cmdArgs)-1; i++ {
				if cmdArgs[i] == tt.expectedArgs[0] && cmdArgs[i+1] == tt.expectedArgs[1] {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Expected args %v in cmdArgs %v", tt.expectedArgs, cmdArgs)
			}
		})
	}

	// 3. Network table cases
	networkTests := []struct {
		name        string
		network     string
		expectArgs  []string
		expectError bool
	}{
		{name: "Default network", network: ""},
		{name: "None network", network: "none", expectArgs: []string{"--network", "none"}},
		{name: "Unsupported network", network: "custom", expectError: true},
	}

	for _, tt := range networkTests {
		t.Run(tt.name, func(t *testing.T) {
			s := Config{
				Image:   "alpine",
				Network: tt.network,
			}
			cmdArgs, err := buildDockerArgs(s, args, false)
			if tt.expectError {
				if err == nil {
					t.Fatalf("expected error for network %q, got nil", tt.network)
				}
				expectedMessage := `network mode "custom" is not supported by the docker sandbox`
				if !strings.Contains(err.Error(), expectedMessage) {
					t.Errorf("got error %q, want %q", err.Error(), expectedMessage)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildDockerArgs failed: %v", err)
			}
			for _, arg := range tt.expectArgs {
				if !slices.Contains(cmdArgs, arg) {
					t.Errorf("expected arg %q in %v", arg, cmdArgs)
				}
			}
			if len(tt.expectArgs) == 0 && slices.Contains(cmdArgs, "--network") {
				t.Errorf("unexpected --network in %v", cmdArgs)
			}
		})
	}
}

func TestBackendExitError(t *testing.T) {
	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand
	t.Setenv("MOCK_BEHAVIOR", "exit_42")

	t.Run("DockerSandbox", func(t *testing.T) {
		sandbox := &dockerSandbox{}
		script := Config{Image: "alpine"}
		var stdout, stderr bytes.Buffer
		err := sandbox.Run(t.Context(), script, []string{"exit42"}, RunOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
		if err == nil {
			t.Fatalf("expected error from failing tool, got nil")
		}

		var exitError *ExitError
		if !errors.As(err, &exitError) {
			t.Fatalf("expected *ExitError, got %T: %v", err, err)
		}
		if exitError.ExitCode() != 42 {
			t.Errorf("expected exit code 42, got %d", exitError.ExitCode())
		}
	})
}

func assertRunCancelled(t *testing.T, runFunction func(ctx context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() {
		runDone <- runFunction(ctx)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-runDone:
		if err == nil {
			t.Fatalf("expected error from cancelled Run, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected error wrapping context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return promptly after context cancellation")
	}
}

func TestDockerContextCancelled(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker binary not found")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not accessible")
	}

	testContainerName := "clix-test-cancel-" + strings.ToLower(t.Name())
	oldGenerateContainerNameFunction := generateContainerNameFunction
	defer func() { generateContainerNameFunction = oldGenerateContainerNameFunction }()
	generateContainerNameFunction = func() (string, error) {
		return testContainerName, nil
	}

	_ = exec.Command("docker", "rm", "-f", testContainerName).Run()
	defer func() {
		_ = exec.Command("docker", "rm", "-f", testContainerName).Run()
	}()

	backend := &dockerSandbox{}
	script := Config{Image: "alpine"}
	ctx, cancel := context.WithCancel(t.Context())

	runDone := make(chan error, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		runDone <- backend.Run(ctx, script, []string{"sleep", "60"}, RunOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	}()

	deadline := time.Now().Add(30 * time.Second)
	containerRunning := false
	for time.Now().Before(deadline) {
		output, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", testContainerName).Output()
		if err == nil && strings.TrimSpace(string(output)) == "true" {
			containerRunning = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !containerRunning {
		t.Fatalf("timed out waiting for container %s to start running", testContainerName)
	}

	cancel()

	select {
	case err := <-runDone:
		if err == nil {
			t.Fatalf("expected error from cancelled Run, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected error wrapping context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return within 5 seconds of context cancellation")
	}

	output, err := exec.Command("docker", "ps", "-a", "--filter", "name="+testContainerName, "--format", "{{.Names}}").Output()
	if err != nil {
		t.Fatalf("failed to query docker ps: %v", err)
	}
	if strings.Contains(string(output), testContainerName) {
		t.Errorf("expected container %s to be gone, but found in docker ps: %s", testContainerName, string(output))
	}
}

func TestBackendContextCancelledMock(t *testing.T) {
	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand
	t.Setenv("MOCK_BEHAVIOR", "sleep")

	oldPrepareRootFSFunction := prepareRootFSFunction
	defer func() { prepareRootFSFunction = oldPrepareRootFSFunction }()
	prepareRootFSFunction = func(ctx context.Context, imageRef string) (*preparedRootFS, error) {
		return &preparedRootFS{
			dir:     t.TempDir(),
			cleanup: func() {},
		}, nil
	}

	env := []EnvVar{
		{Name: "GO_WANT_HELPER_PROCESS", Value: "1"},
		{Name: "MOCK_BEHAVIOR", Value: "sleep"},
	}
	tests := []struct {
		name    string
		backend Sandbox
		config  Config
	}{
		{name: "Docker", backend: &dockerSandbox{}, config: Config{Image: "alpine", Env: env}},
		{name: "AppleContainer", backend: &appleContainerSandbox{}, config: Config{Image: "alpine", Env: env}},
		{name: "Proot", backend: &prootSandbox{}, config: Config{Image: "alpine", Entrypoint: "/bin/sleep", Env: env}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			assertRunCancelled(t, func(ctx context.Context) error {
				var stdout, stderr bytes.Buffer
				return testCase.backend.Run(ctx, testCase.config, []string{"sleep"}, RunOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
			})
		})
	}
}

func TestDockerStopFailureReported(t *testing.T) {
	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand
	t.Setenv("MOCK_BEHAVIOR", "docker_rm_error")

	testContainerName := "clix-test-stop-failure"
	oldGenerateContainerNameFunction := generateContainerNameFunction
	defer func() { generateContainerNameFunction = oldGenerateContainerNameFunction }()
	generateContainerNameFunction = func() (string, error) {
		return testContainerName, nil
	}

	backend := &dockerSandbox{}
	script := Config{Image: "alpine"}
	ctx, cancel := context.WithCancel(t.Context())

	runDone := make(chan error, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		runDone <- backend.Run(ctx, script, []string{"sleep"}, RunOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-runDone:
		if err == nil {
			t.Fatalf("expected error from cancelled Run with stop failure, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected error wrapping context.Canceled, got %v", err)
		}
		expectedMessage := fmt.Sprintf("failed to stop container %s (remove with 'docker rm -f %s')", testContainerName, testContainerName)
		if !strings.Contains(err.Error(), expectedMessage) {
			t.Errorf("expected error to contain %q, got %q", expectedMessage, err.Error())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return promptly after context cancellation")
	}
}

func TestDockerStopTimeoutReported(t *testing.T) {
	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand
	t.Setenv("MOCK_BEHAVIOR", "docker_rm_hang")

	oldStopTimeout := dockerStopTimeout
	defer func() { dockerStopTimeout = oldStopTimeout }()
	dockerStopTimeout = 100 * time.Millisecond

	testContainerName := "clix-test-stop-timeout"
	oldGenerateContainerNameFunction := generateContainerNameFunction
	defer func() { generateContainerNameFunction = oldGenerateContainerNameFunction }()
	generateContainerNameFunction = func() (string, error) {
		return testContainerName, nil
	}

	backend := &dockerSandbox{}
	script := Config{Image: "alpine"}
	ctx, cancel := context.WithCancel(t.Context())

	runDone := make(chan error, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		runDone <- backend.Run(ctx, script, []string{"sleep"}, RunOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-runDone:
		if err == nil {
			t.Fatalf("expected error from cancelled Run with stop timeout, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected error wrapping context.Canceled, got %v", err)
		}
		expectedMessage := fmt.Sprintf("failed to stop container %s within %v (remove with 'docker rm -f %s')", testContainerName, dockerStopTimeout, testContainerName)
		if !strings.Contains(err.Error(), expectedMessage) {
			t.Errorf("expected error to contain %q, got %q", expectedMessage, err.Error())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return promptly after context cancellation")
	}
}

func TestContextAlreadyCancelledBeforeRun(t *testing.T) {
	backend := &dockerSandbox{}
	script := Config{Image: "alpine"}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var stdout, stderr bytes.Buffer
	err := backend.Run(ctx, script, []string{"sleep"}, RunOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	if err == nil {
		t.Fatalf("expected error from cancelled context before run, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected error wrapping context.Canceled, got %v", err)
	}
}

func TestDockerRunsGivenExecutableInsteadOfPath(t *testing.T) {
	// Create a decoy "docker" in PATH that creates a marker and fails if executed.
	pathDirectory := t.TempDir()
	decoyMarkerFile := filepath.Join(pathDirectory, "decoy_executed.txt")
	decoyScript := fmt.Sprintf("#!/bin/sh\necho decoy > %s\nexit 1\n", decoyMarkerFile)
	decoyDockerPath := filepath.Join(pathDirectory, "docker")
	if err := os.WriteFile(decoyDockerPath, []byte(decoyScript), 0755); err != nil {
		t.Fatalf("failed to write decoy docker script: %v", err)
	}
	t.Setenv("PATH", pathDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Create a custom executable outside of PATH.
	customDirectory := t.TempDir()
	argumentsFile := filepath.Join(customDirectory, "arguments.txt")
	customDockerScript := fmt.Sprintf("#!/bin/sh\nfor argument in \"$@\"; do\n  echo \"$argument\"\ndone > %s\nexit 0\n", argumentsFile)
	customDockerPath := filepath.Join(customDirectory, "custom-docker")
	if err := os.WriteFile(customDockerPath, []byte(customDockerScript), 0755); err != nil {
		t.Fatalf("failed to write custom docker script: %v", err)
	}

	backend, err := New("docker", WithExecutable("docker", customDockerPath))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	config := Config{
		Image: "alpine:latest",
	}

	var stdout, stderr bytes.Buffer
	if err := backend.Run(t.Context(), config, []string{"echo", "hello"}, RunOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}); err != nil {
		t.Fatalf("backend.Run failed: %v", err)
	}

	// Verify that the decoy docker on PATH was NOT executed.
	if _, err := os.Stat(decoyMarkerFile); !os.IsNotExist(err) {
		t.Fatalf("decoy docker on PATH was executed instead of custom executable")
	}

	// Verify that the custom executable was executed.
	argumentsContent, err := os.ReadFile(argumentsFile)
	if err != nil {
		t.Fatalf("failed to read arguments file: %v", err)
	}
	actualArguments := strings.Split(strings.TrimSpace(string(argumentsContent)), "\n")
	if len(actualArguments) == 0 || actualArguments[0] != "run" {
		t.Errorf("expected custom executable to receive docker run arguments, got %v", actualArguments)
	}
}

func TestDockerTerminalAllocationWithRunOptions(t *testing.T) {
	interactiveTrue := true
	interactiveFalse := false

	tests := []struct {
		name               string
		options            RunOptions
		terminalStdin      bool
		expectTerminalFlag bool
	}{
		{
			name:               "UnsetWithNonTerminalStdin",
			options:            RunOptions{Stdin: strings.NewReader("")},
			terminalStdin:      false,
			expectTerminalFlag: false,
		},
		{
			name:               "UnsetWithTerminalStdin",
			options:            RunOptions{Stdin: strings.NewReader("")},
			terminalStdin:      true,
			expectTerminalFlag: true,
		},
		{
			name:               "ExplicitTrueWithNonTerminalStdin",
			options:            RunOptions{Stdin: strings.NewReader(""), Interactive: &interactiveTrue},
			terminalStdin:      false,
			expectTerminalFlag: false,
		},
		{
			name:               "ExplicitTrueWithTerminalStdin",
			options:            RunOptions{Stdin: strings.NewReader(""), Interactive: &interactiveTrue},
			terminalStdin:      true,
			expectTerminalFlag: true,
		},
		{
			name:               "ExplicitFalseWithNonTerminalStdin",
			options:            RunOptions{Stdin: strings.NewReader(""), Interactive: &interactiveFalse},
			terminalStdin:      false,
			expectTerminalFlag: false,
		},
		{
			name:               "ExplicitFalseWithTerminalStdin",
			options:            RunOptions{Stdin: strings.NewReader(""), Interactive: &interactiveFalse},
			terminalStdin:      true,
			expectTerminalFlag: false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			oldIsTerminal := isTerminalFunction
			defer func() { isTerminalFunction = oldIsTerminal }()
			isTerminalFunction = func(io.Reader) bool { return testCase.terminalStdin }

			if got := shouldAllocateTerminal(testCase.options); got != testCase.expectTerminalFlag {
				t.Errorf("shouldAllocateTerminal() = %v, want %v", got, testCase.expectTerminalFlag)
			}
		})
	}
}
