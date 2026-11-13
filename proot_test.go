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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunProot(t *testing.T) {
	if _, err := exec.LookPath("proot"); err != nil {
		t.Skip("skipping proot test: proot not found in PATH")
	}

	tmpDir := t.TempDir()

	scriptPath := filepath.Join(tmpDir, "test-script-proot-pull")

	// hello-world binary is at /hello
	scriptContent := `#!/usr/bin/env clix
image: hello-world
entrypoint: /hello
`

	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to write script: %v", err)
	}

	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader("")
	args := []string{"clix", scriptPath}

	os.Setenv("CLIX_SANDBOX", "proot")
	defer os.Unsetenv("CLIX_SANDBOX")

	err := run(stdin, &stdout, &stderr, args)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "Hello from Docker!") {
		t.Errorf("unexpected output: %q", stdout.String())
	}
}

func TestProotReadOnlyMountRejected(t *testing.T) {
	sandbox := &ProotSandbox{}
	script := Script{
		Image: "test-image",
		Mounts: []Mount{
			{HostPath: "/host", SandboxPath: "/sandbox", ReadOnly: true},
		},
	}

	err := sandbox.Run(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, script, []string{"echo", "hi"})
	if err == nil {
		t.Fatalf("expected error for read-only mount in proot, got nil")
	}
	expected := "read-only mounts are not supported by the proot sandbox"
	if !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}

func TestProotRepoRootMount(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoDir, ".git"), 0755); err != nil {
		t.Fatalf("failed to create .git: %v", err)
	}
	t.Chdir(repoDir)

	oldPrepareRootFS := prepareRootFSFn
	defer func() { prepareRootFSFn = oldPrepareRootFS }()
	prepareRootFSFn = func(imageRef string) (*preparedRootFS, error) {
		return &preparedRootFS{
			dir:      "/mock/rootfs",
			imageSHA: "mocksha",
			cleanup:  func() {},
		}, nil
	}

	var capturedName string
	var capturedArgs []string
	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		capturedName = name
		capturedArgs = args
		return fakeExecCommand(name, args...)
	}

	sandbox := &ProotSandbox{}
	script := Script{
		Image: "mock-image",
		Mounts: []Mount{
			{HostPath: "git.repoRoot(cwd)", SandboxPath: "/workspace"},
		},
	}

	var stdout, stderr bytes.Buffer
	err := sandbox.Run(strings.NewReader(""), &stdout, &stderr, script, []string{"echo", "hello"})
	if err != nil {
		t.Fatalf("sandbox.Run failed: %v", err)
	}

	if capturedName != "proot" {
		t.Errorf("expected command proot, got %q", capturedName)
	}

	expectedRootBind := repoDir + ":/workspace"
	expectedGitBind := filepath.Join(repoDir, ".git") + ":/workspace/.git"

	hasRootBind := false
	hasGitBind := false
	for i := 0; i < len(capturedArgs)-1; i++ {
		if capturedArgs[i] == "-b" {
			if capturedArgs[i+1] == expectedRootBind {
				hasRootBind = true
			}
			if capturedArgs[i+1] == expectedGitBind {
				hasGitBind = true
			}
		}
	}

	if !hasRootBind {
		t.Errorf("expected proot arg -b %s, got args: %v", expectedRootBind, capturedArgs)
	}
	if !hasGitBind {
		t.Errorf("expected proot arg -b %s, got args: %v", expectedGitBind, capturedArgs)
	}

	expectedWarning := "warning: proot sandbox cannot protect .git from writes\n"
	if !strings.Contains(stderr.String(), expectedWarning) {
		t.Errorf("expected stderr to contain %q, got %q", expectedWarning, stderr.String())
	}
}
