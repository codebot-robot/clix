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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProotReadOnlyMountRejected(t *testing.T) {
	sandbox := &prootSandbox{}
	script := Config{
		Image: "test-image",
		Mounts: []Mount{
			{HostPath: "/host", SandboxPath: "/sandbox", ReadOnly: true},
		},
	}

	err := sandbox.Run(t.Context(), script, []string{"echo", "hi"}, RunOptions{Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err == nil {
		t.Fatalf("expected error for read-only mount in proot, got nil")
	}
	expected := "read-only mounts are not supported by the proot sandbox"
	if !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}

func TestProotReadOnlyMountDeclaredByScriptRejectedEvenIfNamedGit(t *testing.T) {
	sandbox := &prootSandbox{}
	script := Config{
		Image: "test-image",
		Mounts: []Mount{
			{HostPath: "/custom/path/.git", SandboxPath: "/sandbox/.git", ReadOnly: true},
		},
	}

	err := sandbox.Run(t.Context(), script, []string{"echo", "hi"}, RunOptions{Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err == nil {
		t.Fatalf("expected error for script-declared read-only mount ending in .git, got nil")
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

	oldPrepareRootFS := prepareRootFSFunction
	defer func() { prepareRootFSFunction = oldPrepareRootFS }()
	prepareRootFSFunction = func(ctx context.Context, imageRef string) (*preparedRootFS, error) {
		return &preparedRootFS{
			dir:     "/mock/rootfs",
			cleanup: func() {},
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

	sandbox := &prootSandbox{}
	script := Config{
		Image: "mock-image",
		Mounts: []Mount{
			{HostPath: repoDir, SandboxPath: "/workspace"},
			{HostPath: filepath.Join(repoDir, ".git"), SandboxPath: "/workspace/.git", ReadOnly: true, WritableFallback: true},
		},
	}

	var stdout, stderr bytes.Buffer
	err := sandbox.Run(t.Context(), script, []string{"echo", "hello"}, RunOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
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

func TestProotFailingToolCleansUpRootFS(t *testing.T) {
	temporaryDirectory := t.TempDir()

	oldPrepareRootFS := prepareRootFSFunction
	defer func() { prepareRootFSFunction = oldPrepareRootFS }()
	prepareRootFSFunction = func(ctx context.Context, imageRef string) (*preparedRootFS, error) {
		return &preparedRootFS{
			dir: temporaryDirectory,
			cleanup: func() {
				if err := os.RemoveAll(temporaryDirectory); err != nil {
					t.Errorf("failed to remove temp dir: %v", err)
				}
			},
		}, nil
	}

	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand

	sandbox := &prootSandbox{}
	script := Config{
		Image: "mock-image",
		Env: []EnvVar{
			{Name: "GO_WANT_HELPER_PROCESS", Value: "1"},
			{Name: "MOCK_BEHAVIOR", Value: "exit_42"},
		},
	}

	err := sandbox.Run(t.Context(), script, []string{"failing-tool"}, RunOptions{Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	var exitError *ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 42 {
		t.Fatalf("expected ExitError with code 42, got %v", err)
	}

	if _, err := os.Stat(temporaryDirectory); !os.IsNotExist(err) {
		t.Errorf("expected temporary rootfs %s to be removed, but it still exists", temporaryDirectory)
	}
}
