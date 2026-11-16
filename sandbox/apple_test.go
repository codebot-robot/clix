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
	"testing"
)

func TestBuildAppleContainerArgs(t *testing.T) {
	script := Config{
		Image: "python:3.11",
		Env: []EnvVar{
			{Name: "VAR_A", Value: "VAL_A"},
		},
	}
	args := []string{"script.py"}

	cmdArgs, err := buildAppleContainerArgs(script, args, false)
	if err != nil {
		t.Fatalf("buildAppleContainerArgs failed: %v", err)
	}

	// Check basics
	// cmdArgs: [run --rm -w ... -e ... image args...]
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
		if arg == "-v" {
			t.Errorf("Unexpected '-v' in cmdArgs: %v", cmdArgs)
		}
	}

	if !foundRun {
		t.Errorf("Expected 'run' in args, got %v", cmdArgs)
	}
	if !foundRm {
		t.Errorf("Expected '--rm' in args, got %v", cmdArgs)
	}
	if !foundImage {
		t.Errorf("Expected image 'python:3.11' in args, got %v", cmdArgs)
	}
	if !foundEnv {
		t.Errorf("Expected environment variable VAR_A=VAL_A in args, got %v", cmdArgs)
	}

	termArgs, err := buildAppleContainerArgs(script, args, true)
	if err != nil {
		t.Fatalf("buildAppleContainerArgs with isTerm=true failed: %v", err)
	}
	foundTerm := false
	for _, arg := range termArgs {
		if arg == "-it" {
			foundTerm = true
		}
	}
	if !foundTerm {
		t.Errorf("Expected '-it' in termArgs: %v", termArgs)
	}

	// Mount read-only table cases
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
			expectedArgs: []string{"--mount", "type=bind,source=/host/path,target=/sandbox/path,readonly"},
		},
		{
			name: "Read-write mount",
			mounts: []Mount{
				{HostPath: "/host/path", SandboxPath: "/sandbox/path", ReadOnly: false},
			},
			expectedArgs: []string{"--mount", "type=bind,source=/host/path,target=/sandbox/path"},
		},
	}

	for _, tt := range mountTests {
		t.Run(tt.name, func(t *testing.T) {
			s := Config{
				Image:  "python:3.11",
				Mounts: tt.mounts,
			}
			cmdArgs, err := buildAppleContainerArgs(s, args, false)
			if err != nil {
				t.Fatalf("buildAppleContainerArgs failed: %v", err)
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
			for _, arg := range cmdArgs {
				if arg == "-v" {
					t.Errorf("Unexpected '-v' in cmdArgs: %v", cmdArgs)
				}
			}
		})
	}
}
