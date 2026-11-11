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

	"sigs.k8s.io/yaml"
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

	// 4. Mount read-only table cases
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
			s := Script{
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

func TestBuildImage(t *testing.T) {
	execCommand = fakeExecCommand
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

func TestRunUnknownFields(t *testing.T) {
	var execCalled bool
	oldExecCommand := execCommand
	execCommand = func(name string, arg ...string) *exec.Cmd {
		execCalled = true
		return oldExecCommand(name, arg...)
	}
	defer func() { execCommand = oldExecCommand }()

	tests := []struct {
		name          string
		scriptContent string
		expectedError string
	}{
		{
			name: "Unknown top-level key",
			scriptContent: `#!/usr/bin/env clix
image: alpine
unknownField: foo
`,
			expectedError: "unknownField",
		},
		{
			name: "Unknown key inside mount",
			scriptContent: `#!/usr/bin/env clix
image: alpine
mounts:
- hostPath: /tmp
  sandboxPath: /tmp
  unknownMountKey: bar
`,
			expectedError: "unknownMountKey",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			execCalled = false
			tmpDir := t.TempDir()
			scriptPath := filepath.Join(tmpDir, "script.yaml")
			if err := os.WriteFile(scriptPath, []byte(tt.scriptContent), 0755); err != nil {
				t.Fatalf("Failed to write script: %v", err)
			}

			var stdout, stderr bytes.Buffer
			stdin := strings.NewReader("")
			err := run(stdin, &stdout, &stderr, []string{"clix", scriptPath})
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.expectedError) {
				t.Errorf("expected error to contain %q, got %q", tt.expectedError, err.Error())
			}
			if execCalled {
				t.Errorf("expected no command to be executed, but execCommand was called")
			}
		})
	}
}

func TestExamplesParse(t *testing.T) {
	entries, err := os.ReadDir("examples")
	if err != nil {
		t.Fatalf("Failed to read examples dir: %v", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			path := filepath.Join("examples", entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("Failed to read %s: %v", path, err)
			}
			var script Script
			if err := yaml.UnmarshalStrict(data, &script); err != nil {
				t.Errorf("Failed to strictly unmarshal %s: %v", path, err)
			}
		})
	}
}

func TestRunReadOnlyMount(t *testing.T) {
	tests := []struct {
		name          string
		scriptContent string
		expectedArg   string
	}{
		{
			name: "readOnly true in yaml",
			scriptContent: `#!/usr/bin/env clix
image: alpine
mounts:
- hostPath: /tmp/host
  sandboxPath: /tmp/sandbox
  readOnly: true
`,
			expectedArg: "/tmp/host:/tmp/sandbox:ro",
		},
		{
			name: "readOnly false in yaml",
			scriptContent: `#!/usr/bin/env clix
image: alpine
mounts:
- hostPath: /tmp/host
  sandboxPath: /tmp/sandbox
  readOnly: false
`,
			expectedArg: "/tmp/host:/tmp/sandbox",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedArgs []string
			oldExecCommand := execCommand
			execCommand = func(name string, arg ...string) *exec.Cmd {
				capturedArgs = arg
				return fakeExecCommand(name, arg...)
			}
			defer func() { execCommand = oldExecCommand }()

			tmpDir := t.TempDir()
			scriptPath := filepath.Join(tmpDir, "script.yaml")
			if err := os.WriteFile(scriptPath, []byte(tt.scriptContent), 0755); err != nil {
				t.Fatalf("Failed to write script: %v", err)
			}

			var stdout, stderr bytes.Buffer
			stdin := strings.NewReader("")
			if err := run(stdin, &stdout, &stderr, []string{"clix", scriptPath}); err != nil {
				t.Fatalf("run failed: %v", err)
			}

			foundMount := false
			for i := 0; i < len(capturedArgs)-1; i++ {
				if capturedArgs[i] == "-v" && capturedArgs[i+1] == tt.expectedArg {
					foundMount = true
					break
				}
			}
			if !foundMount {
				t.Errorf("expected mount arg %q in %v", tt.expectedArg, capturedArgs)
			}
		})
	}
}
