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

package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestBuildAppleContainerArgs(t *testing.T) {
	// Mock getAppleContainerImageSHA
	originalGetImageSHA := getAppleContainerImageSHAFn
	defer func() { getAppleContainerImageSHAFn = originalGetImageSHA }()
	getAppleContainerImageSHAFn = func(image string) (string, error) {
		return "mocksha256", nil
	}

	script := Script{
		Image: "python:3.11",
		Mounts: []Mount{
			{HostPath: "${cacheDir}/python", SandboxPath: "/tmp/.clix-pycache"},
		},
		Env: []EnvVar{
			{Name: "PYTHONPYCACHEPREFIX", Value: "/tmp/.clix-pycache"},
		},
	}
	args := []string{"script.py"}

	cmdArgs, err := buildAppleContainerArgs(script, args, false)
	if err != nil {
		t.Fatalf("buildAppleContainerArgs failed: %v", err)
	}

	// Check basics
	// cmdArgs: [run --rm -w ... -v ... -e ... image args...]
	foundRun := false
	foundRm := false
	foundImage := false
	foundEnv := false
	foundMount := false

	cacheMountDest := "/tmp/.clix-pycache"
	envVar := "PYTHONPYCACHEPREFIX=" + cacheMountDest
	expectedHostPathPart := "mocksha256/python"

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
		if arg == "-e" && i+1 < len(cmdArgs) && cmdArgs[i+1] == envVar {
			foundEnv = true
		}
		if arg == "-v" && i+1 < len(cmdArgs) && strings.Contains(cmdArgs[i+1], ":"+cacheMountDest) {
			if strings.Contains(cmdArgs[i+1], expectedHostPathPart) {
				foundMount = true
			}
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
		t.Errorf("Expected environment variable %s, got args: %v", envVar, cmdArgs)
	}
	if !foundMount {
		t.Errorf("Expected mount for %s with host path containing %s, got args: %v", cacheMountDest, expectedHostPathPart, cmdArgs)
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
			expectedArgs: []string{"-v", "/host/path:/sandbox/path"},
		},
	}

	for _, tt := range mountTests {
		t.Run(tt.name, func(t *testing.T) {
			s := Script{
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
		})
	}
}

func TestBuildImageAppleContainer(t *testing.T) {
	oldExec := execCommand
	defer func() { execCommand = oldExec }()

	execCommand = fakeExecCommand

	os.Setenv("CLIX_SANDBOX", "apple-container")
	defer os.Unsetenv("CLIX_SANDBOX")

	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader("")

	build := &BuildConfig{
		Git: "https://github.com/example/repo",
	}

	_, err := buildImage(stdin, &stdout, &stderr, build, "test-script.yaml")
	if err != nil {
		t.Fatalf("buildImage failed: %v", err)
	}

	outStr := stderr.String()
	if !strings.Contains(outStr, "Cloning") {
		t.Errorf("Expected cloning message, got: %s", outStr)
	}
	if !strings.Contains(outStr, "Building image") {
		t.Errorf("Expected building message, got: %s", outStr)
	}
}

func TestGetAppleContainerImageSHA(t *testing.T) {
	// Mock execCommand
	oldExec := execCommand
	defer func() { execCommand = oldExec }()

	execCommand = fakeExecCommand

	sha, err := getAppleContainerImageSHA("test-image")
	if err != nil {
		t.Fatalf("getAppleContainerImageSHA failed: %v", err)
	}

	expectedSHA := "abcdef123456"
	if sha != expectedSHA {
		t.Errorf("Expected SHA %s, got %s", expectedSHA, sha)
	}
}
