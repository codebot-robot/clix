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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	// Create a temporary script file
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test-script")

	// The script points to our local test-tool
	// We need absolute path to test-tool for it to work reliably from anywhere
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get cwd: %v", err)
	}
	testToolPath := filepath.Join(cwd, "tests", "test-tool")

	scriptContent := fmt.Sprintf(`#!/usr/bin/env clix
go:
  run: %s
`,
		testToolPath)

	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to write script: %v", err)
	}

	// Capture output
	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader("")

	args := []string{"clix", scriptPath, "foo", "bar"}

	err = run(stdin, &stdout, &stderr, args)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	output := stdout.String()
	expectedOutput := "Hello from test-tool"
	if !strings.Contains(output, expectedOutput) {
		t.Errorf("Expected output to contain %q, got %q", expectedOutput, output)
	}

	if !strings.Contains(output, "Arg 0: foo") {
		t.Errorf("Expected output to contain 'Arg 0: foo', got %q", output)
	}
}

func TestRunDocker(t *testing.T) {
	_, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker not found")
	}

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test-script-docker")

	scriptContent := `#!/usr/bin/env clix
image: alpine
entrypoint: echo
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to write script: %v", err)
	}

	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader("")

	// We use 'echo' as entrypoint and pass 'hello' as arg.
	// Expected output: hello
	args := []string{"clix", scriptPath, "hello"}

	err = run(stdin, &stdout, &stderr, args)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "hello") {
		t.Errorf("Expected output to contain 'hello', got %q", output)
	}
}

func TestResolveMounts(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get user home: %v", err)
	}

	tests := []struct {
		name     string
		input    []Mount
		imageSHA string
		expected []Mount
	}{
		{
			name: "Home directory expansion",
			input: []Mount{
				{HostPath: "~/.config"},
				{HostPath: "~/data"},
				{HostPath: "~"},
			},
			expected: []Mount{
				{HostPath: filepath.Join(home, ".config"), SandboxPath: filepath.Join(home, ".config")},
				{HostPath: filepath.Join(home, "data"), SandboxPath: filepath.Join(home, "data")},
				{HostPath: home, SandboxPath: home},
			},
		},
		{
			name: "No expansion",
			input: []Mount{
				{HostPath: "/absolute/path"},
			},
			expected: []Mount{
				{HostPath: "/absolute/path", SandboxPath: "/absolute/path"},
			},
		},
		{
			name: "Sandbox path tilde expansion",
			input: []Mount{
				{HostPath: "/tmp", SandboxPath: "~/.config"},
				{HostPath: "/tmp", SandboxPath: "~/data"},
				{HostPath: "/tmp", SandboxPath: "~"},
			},
			expected: []Mount{
				{HostPath: "/tmp", SandboxPath: "/root/.config"},
				{HostPath: "/tmp", SandboxPath: "/root/data"},
				{HostPath: "/tmp", SandboxPath: "/root"},
			},
		},
		{
			name:     "Cache directory expansion",
			input:    []Mount{{HostPath: "${cacheDir}/pycache"}, {HostPath: "{cacheDir}/oldcache"}},
			imageSHA: "abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveMounts(tt.input, tt.imageSHA)
			if err != nil {
				t.Fatalf("resolveMounts failed: %v", err)
			}

			if tt.name == "Cache directory expansion" {
				for _, m := range got {
					if strings.Contains(m.HostPath, "cacheDir") {
						t.Errorf("Expected cacheDir to be expanded, got %q", m.HostPath)
					}
					if !strings.Contains(m.HostPath, "abc") {
						t.Errorf("Expected host path to contain image SHA 'abc', got %q", m.HostPath)
					}
				}
				return
			}

			if len(got) != len(tt.expected) {
				t.Fatalf("expected %d mounts, got %d", len(tt.expected), len(got))
			}

			for i, m := range got {
				if m.HostPath != tt.expected[i].HostPath {
					t.Errorf("mount[%d].HostPath = %q, want %q", i, m.HostPath, tt.expected[i].HostPath)
				}
				if m.SandboxPath != tt.expected[i].SandboxPath {
					t.Errorf("mount[%d].SandboxPath = %q, want %q", i, m.SandboxPath, tt.expected[i].SandboxPath)
				}
			}
		})
	}
}

func TestBuildDockerArgs(t *testing.T) {
	// Mock getImageSHA
	originalGetImageSHA := getImageSHAFn
	defer func() { getImageSHAFn = originalGetImageSHA }()
	getImageSHAFn = func(image string) (string, error) {
		return "mocksha256", nil
	}

	// 1. Basic case
	script := Script{
		Image: "python:3.11",
	}
	args := []string{"script.py"}
	cmdArgs, err := buildDockerArgs(script, args, false)
	if err != nil {
		t.Fatalf("buildDockerArgs failed: %v", err)
	}
	// Check basics
	// cmdArgs: [run -i -w ... image args...]
	foundImage := false
	for _, arg := range cmdArgs {
		if arg == "python:3.11" {
			foundImage = true
		}
	}
	if !foundImage {
		t.Errorf("Expected image python:3.11 in args, got %v", cmdArgs)
	}

	// 2. Python cache enabled via explicit mounts and env
	scriptPython := Script{
		Image: "python:3.11",
		Mounts: []Mount{
			{HostPath: "{cacheDir}/python", SandboxPath: "/tmp/.clix-pycache"},
		},
		Env: []EnvVar{
			{Name: "PYTHONPYCACHEPREFIX", Value: "/tmp/.clix-pycache"},
		},
	}
	cmdArgs, err = buildDockerArgs(scriptPython, args, false)
	if err != nil {
		t.Fatalf("buildDockerArgs failed: %v", err)
	}

	// Check for env var and mount
	foundEnv := false
	foundMount := false

	cacheMountDest := "/tmp/.clix-pycache"
	envVar := "PYTHONPYCACHEPREFIX=" + cacheMountDest

	// We expect the mount path to contain the SHA
	expectedHostPathPart := "mocksha256/python"

	for i, arg := range cmdArgs {
		if arg == "-e" && i+1 < len(cmdArgs) && cmdArgs[i+1] == envVar {
			foundEnv = true
		}
		if arg == "-v" && i+1 < len(cmdArgs) && strings.Contains(cmdArgs[i+1], ":"+cacheMountDest) {
			if strings.Contains(cmdArgs[i+1], expectedHostPathPart) {
				foundMount = true
			}
		}
	}

	if !foundEnv {
		t.Errorf("Expected environment variable %s, got args: %v", envVar, cmdArgs)
	}
	if !foundMount {
		t.Errorf("Expected mount for %s with host path containing %s, got args: %v", cacheMountDest, expectedHostPathPart, cmdArgs)
	}

	// 3. Python cache enabled via explicit mounts and env using ${cacheDir}
	scriptPythonNew := Script{
		Image: "python:3.11",
		Mounts: []Mount{
			{HostPath: "${cacheDir}/python", SandboxPath: "/tmp/.clix-pycache"},
		},
		Env: []EnvVar{
			{Name: "PYTHONPYCACHEPREFIX", Value: "/tmp/.clix-pycache"},
		},
	}
	cmdArgs, err = buildDockerArgs(scriptPythonNew, args, false)
	if err != nil {
		t.Fatalf("buildDockerArgs failed: %v", err)
	}

	// Check for env var and mount
	foundEnv = false
	foundMount = false

	for i, arg := range cmdArgs {
		if arg == "-e" && i+1 < len(cmdArgs) && cmdArgs[i+1] == envVar {
			foundEnv = true
		}
		if arg == "-v" && i+1 < len(cmdArgs) && strings.Contains(cmdArgs[i+1], ":"+cacheMountDest) {
			if strings.Contains(cmdArgs[i+1], expectedHostPathPart) {
				foundMount = true
			}
		}
	}

	if !foundEnv {
		t.Errorf("Expected environment variable %s, got args: %v", envVar, cmdArgs)
	}
	if !foundMount {
		t.Errorf("Expected mount for %s with host path containing %s, got args: %v", cacheMountDest, expectedHostPathPart, cmdArgs)
	}
}

func TestBuildImage(t *testing.T) {
	var recorded [][]string
	execCommand = func(name string, args ...string) *exec.Cmd {
		recorded = append(recorded, append([]string{name}, args...))
		return fakeExecCommand(name, args...)
	}
	defer func() { execCommand = exec.Command }()

	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader("")

	// Test 1: Image does not exist, should build
	build := &BuildConfig{
		Git: "https://github.com/example/repo",
	}

	imageTag, err := buildImage(stdin, &stdout, &stderr, build, "test-script.yaml")
	if err != nil {
		t.Fatalf("buildImage failed: %v", err)
	}

	// Check if image tag is correct
	// Hash of https://github.com/example/repo
	// We expect clix-repo-<hash>:abcdef1234567890
	// We expect clix-test-script-<hash>:abcdef1234567890
	if !strings.HasPrefix(imageTag, "clix-test-script-") {
		t.Errorf("Unexpected image tag prefix: %s", imageTag)
	}
	if !strings.HasSuffix(imageTag, ":abcdef1234567890") {
		t.Errorf("Unexpected image tag suffix: %s", imageTag)
	}

	// Check output
	outStr := stderr.String()
	if !strings.Contains(outStr, "Cloning") {
		t.Errorf("Expected cloning message, got: %s", outStr)
	}
	if !strings.Contains(outStr, "Building image") {
		t.Errorf("Expected building message, got: %s", outStr)
	}

	var lsRemoteCmd, cloneCmd []string
	for _, cmd := range recorded {
		if len(cmd) > 1 && cmd[0] == "git" {
			switch cmd[1] {
			case "ls-remote":
				lsRemoteCmd = cmd
			case "clone":
				cloneCmd = cmd
			}
		}
	}
	if lsRemoteCmd == nil {
		t.Fatal("git ls-remote was not executed")
	}
	assertDashDashBeforeRepo(t, lsRemoteCmd, build.Git)

	if cloneCmd == nil {
		t.Fatal("git clone was not executed")
	}
	assertDashDashBeforeRepo(t, cloneCmd, build.Git)
}

func TestBuildImage_WithBranch(t *testing.T) {
	var recorded [][]string
	execCommand = func(name string, args ...string) *exec.Cmd {
		recorded = append(recorded, append([]string{name}, args...))
		return fakeExecCommand(name, args...)
	}
	defer func() { execCommand = exec.Command }()

	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader("")

	build := &BuildConfig{
		Git:    "https://github.com/example/repo",
		Branch: "feature-branch",
	}

	_, err := buildImage(stdin, &stdout, &stderr, build, "test-script.yaml")
	if err != nil {
		t.Fatalf("buildImage failed: %v", err)
	}

	var lsRemoteCmd, cloneCmd []string
	for _, cmd := range recorded {
		if len(cmd) > 1 && cmd[0] == "git" {
			switch cmd[1] {
			case "ls-remote":
				lsRemoteCmd = cmd
			case "clone":
				cloneCmd = cmd
			}
		}
	}
	if lsRemoteCmd == nil {
		t.Fatal("git ls-remote was not executed")
	}
	assertDashDashBeforeRepo(t, lsRemoteCmd, build.Git)

	if cloneCmd == nil {
		t.Fatal("git clone was not executed")
	}
	assertDashDashBeforeRepo(t, cloneCmd, build.Git)
}

func TestBuildImage_DashRepo(t *testing.T) {
	var recorded [][]string
	execCommand = func(name string, args ...string) *exec.Cmd {
		recorded = append(recorded, append([]string{name}, args...))
		return fakeExecCommand(name, args...)
	}
	defer func() { execCommand = exec.Command }()

	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader("")

	build := &BuildConfig{
		Git: "-invalid-repo",
	}

	_, err := buildImage(stdin, &stdout, &stderr, build, "test-script.yaml")
	if err == nil {
		t.Fatal("expected error for build.git starting with '-', got nil")
	}
	if len(recorded) != 0 {
		t.Errorf("expected no commands to be executed, but got: %v", recorded)
	}
}

func assertDashDashBeforeRepo(t *testing.T, cmd []string, repo string) {
	t.Helper()
	repoIdx := -1
	for i, arg := range cmd {
		if arg == repo {
			repoIdx = i
			break
		}
	}
	if repoIdx == -1 {
		t.Fatalf("repo %q not found in command: %v", repo, cmd)
	}
	if repoIdx == 0 || cmd[repoIdx-1] != "--" {
		t.Errorf("expected '--' immediately before repo %q, got: %v", repo, cmd)
	}
}

func TestBuildImage_Exists(t *testing.T) {

	execCommand = fakeExecCommand

	defer func() { execCommand = exec.Command }()

	os.Setenv("MOCK_BEHAVIOR", "image_exists")

	defer os.Unsetenv("MOCK_BEHAVIOR")

	var stdout, stderr bytes.Buffer

	stdin := strings.NewReader("")

	build := &BuildConfig{

		Git: "https://github.com/example/repo",
	}

	imageTag, err := buildImage(stdin, &stdout, &stderr, build, "test-script.yaml")

	if err != nil {

		t.Fatalf("buildImage failed: %v", err)

	}

	// Output should NOT contain cloning

	outStr := stderr.String()

	if strings.Contains(outStr, "Cloning") {

		t.Errorf("Did not expect cloning message, got: %s", outStr)

	}

	if strings.Contains(outStr, "Building image") {

		t.Errorf("Did not expect building message, got: %s", outStr)

	}

	// Tag should still be returned

	// We expect clix-test-script-<hash>:abcdef1234567890
	if !strings.HasPrefix(imageTag, "clix-test-script-") {

		t.Errorf("Unexpected image tag: %s", imageTag)

	}

}
