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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProotReadOnlyMountRejected(t *testing.T) {
	selectedSandbox := &ProotSandbox{}
	config := Config{
		Image: "test-image",
		Mounts: []Mount{
			{HostPath: "/tmp/host", SandboxPath: "/tmp/sandbox", ReadOnly: true},
		},
	}

	err := selectedSandbox.Run(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, config, []string{"echo", "hi"})
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
	oldExecCommand := ExecCommand
	defer func() { ExecCommand = oldExecCommand }()
	ExecCommand = func(name string, args ...string) *exec.Cmd {
		capturedName = name
		capturedArgs = args
		return fakeExecCommand(name, args...)
	}

	selectedSandbox := &ProotSandbox{}
	config := Config{
		Image: "mock-image",
		Mounts: []Mount{
			{HostPath: repoDir, SandboxPath: "/workspace", ReadOnly: false},
			{HostPath: filepath.Join(repoDir, ".git"), SandboxPath: "/workspace/.git", ReadOnly: true},
		},
	}

	var stdout, stderr bytes.Buffer
	err := selectedSandbox.Run(strings.NewReader(""), &stdout, &stderr, config, []string{"echo", "hello"})
	if err != nil {
		t.Fatalf("selectedSandbox.Run failed: %v", err)
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
