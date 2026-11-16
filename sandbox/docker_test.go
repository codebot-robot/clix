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
	"strings"
	"testing"
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
		err := sandbox.Run(context.Background(), strings.NewReader(""), &stdout, &stderr, script, []string{"exit42"})
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
