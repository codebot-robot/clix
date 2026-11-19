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

package sandbox_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gke-labs/clix/sandbox"
)

func TestLibraryExportedSurface(t *testing.T) {
	temporaryDirectory := t.TempDir()
	argumentsFile := filepath.Join(temporaryDirectory, "arguments.txt")
	fakeDockerScript := fmt.Sprintf("#!/bin/sh\nfor argument in \"$@\"; do\n  echo \"$argument\"\ndone > %s\nif [ -n \"$MOCK_EXIT_CODE\" ]; then\n  exit \"$MOCK_EXIT_CODE\"\nfi\n", argumentsFile)
	fakeDockerPath := filepath.Join(temporaryDirectory, "docker")
	if err := os.WriteFile(fakeDockerPath, []byte(fakeDockerScript), 0755); err != nil {
		t.Fatalf("failed to write fake docker script: %v", err)
	}
	t.Setenv("PATH", temporaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	backend, err := sandbox.New("docker")
	if err != nil {
		t.Fatalf("sandbox.New failed: %v", err)
	}

	currentWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get current working directory: %v", err)
	}

	config := sandbox.Config{
		Image:      "alpine:latest",
		Entrypoint: "/bin/sh",
		Mounts:     []sandbox.Mount{{HostPath: "/host/path", SandboxPath: "/sandbox/path", ReadOnly: true}},
		Env:        []sandbox.EnvVar{{Name: "TEST_VAR", Value: "test-value"}},
	}

	var stdout, stderr bytes.Buffer
	runArguments := []string{"-c", "echo hello"}
	if err := backend.Run(t.Context(), config, runArguments, sandbox.RunOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}); err != nil {
		t.Fatalf("backend.Run failed: %v", err)
	}

	argumentsContent, err := os.ReadFile(argumentsFile)
	if err != nil {
		t.Fatalf("failed to read arguments file: %v", err)
	}
	actualArguments := strings.Split(strings.TrimSpace(string(argumentsContent)), "\n")
	nameIndex := slices.Index(actualArguments, "--name")
	if nameIndex == -1 || nameIndex+1 >= len(actualArguments) || !strings.HasPrefix(actualArguments[nameIndex+1], "clix-") {
		t.Fatalf("expected --name clix-..., got %v", actualArguments)
	}
	actualArguments = append(actualArguments[:nameIndex], actualArguments[nameIndex+2:]...)
	expectedArguments := []string{
		"run", "--rm", "-i",
		"-v", "/host/path:/sandbox/path:ro",
		"-e", "TEST_VAR=test-value",
		"-w", currentWorkingDirectory,
		"--entrypoint", "/bin/sh",
		"alpine:latest",
		"-c", "echo hello",
	}
	if !slices.Equal(actualArguments, expectedArguments) {
		t.Errorf("got arguments %v, want %v", actualArguments, expectedArguments)
	}

	t.Run("NonZeroExitReportedAsExitError", func(t *testing.T) {
		t.Setenv("MOCK_EXIT_CODE", "42")
		err := backend.Run(t.Context(), config, runArguments, sandbox.RunOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
		var exitError *sandbox.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 42 {
			t.Fatalf("expected ExitError with exit code 42, got %v", err)
		}
	})

	t.Run("FromEnvironment", func(t *testing.T) {
		t.Setenv("CLIX_SANDBOX", "docker")
		backendFromEnv, err := sandbox.FromEnvironment()
		if err != nil || backendFromEnv == nil {
			t.Fatalf("expected valid backend from environment, got %v", err)
		}
	})

	t.Run("UnrecognisedBackendReturnsError", func(t *testing.T) {
		if _, err := sandbox.New("nonexistent"); err == nil {
			t.Fatal("expected error for unrecognised backend, got nil")
		}
	})

	t.Run("WithExecutable", func(t *testing.T) {
		backendWithExecutable, err := sandbox.New("docker", sandbox.WithExecutable("docker", fakeDockerPath))
		if err != nil {
			t.Fatalf("sandbox.New with WithExecutable failed: %v", err)
		}
		var customStdout, customStderr bytes.Buffer
		if err := backendWithExecutable.Run(t.Context(), config, runArguments, sandbox.RunOptions{Stdin: strings.NewReader(""), Stdout: &customStdout, Stderr: &customStderr}); err != nil {
			t.Fatalf("backendWithExecutable.Run failed: %v", err)
		}
	})

	t.Run("RunOptionsInteractive", func(t *testing.T) {
		for _, interactive := range []bool{true, false} {
			var customOutput bytes.Buffer
			isInteractive := interactive
			if err := backend.Run(t.Context(), config, runArguments, sandbox.RunOptions{
				Stdin:       strings.NewReader(""),
				Stdout:      &customOutput,
				Stderr:      &customOutput,
				Interactive: &isInteractive,
			}); err != nil {
				t.Fatalf("backend.Run with Interactive=%v failed: %v", interactive, err)
			}

			argumentsData, err := os.ReadFile(argumentsFile)
			if err != nil {
				t.Fatalf("failed to read arguments file: %v", err)
			}
			actualArgs := strings.Split(strings.TrimSpace(string(argumentsData)), "\n")
			if slices.Contains(actualArgs, "-t") {
				t.Errorf("Interactive=%v: expected no -t when Stdin is not a terminal, got arguments %v", interactive, actualArgs)
			}
		}
	})

	t.Run("FirstUsableAndCheckUsable", func(t *testing.T) {
		firstBackend, err := sandbox.FirstUsable(t.Context(), sandbox.WithExecutable("docker", fakeDockerPath))
		if err != nil {
			t.Fatalf("sandbox.FirstUsable failed: %v", err)
		}
		if firstBackend == nil {
			t.Fatal("expected non-nil backend from FirstUsable")
		}
		if firstBackend.String() == "" {
			t.Errorf("expected non-empty backend name, got %q", firstBackend.String())
		}
		if err := firstBackend.Usable(t.Context()); err != nil {
			t.Errorf("expected backend to report usable, got %v", err)
		}

		if err := sandbox.CheckUsable(t.Context(), "docker", sandbox.WithExecutable("docker", fakeDockerPath)); err != nil {
			t.Errorf("sandbox.CheckUsable failed: %v", err)
		}
	})
}
