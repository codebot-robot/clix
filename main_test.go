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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/clix/sandbox"
	"github.com/go-logr/logr"
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
	userCache, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("failed to get user cache: %v", err)
	}

	tests := []struct {
		name        string
		input       []sandbox.Mount
		imageSHA    string
		scriptKey   string
		expected    []sandbox.Mount
		wantErr     bool
		errContains string
	}{
		{
			name: "Home directory expansion",
			input: []sandbox.Mount{
				{HostPath: "~/.config"},
				{HostPath: "~/data"},
				{HostPath: "~"},
			},
			expected: []sandbox.Mount{
				{HostPath: filepath.Join(home, ".config"), SandboxPath: filepath.Join(home, ".config")},
				{HostPath: filepath.Join(home, "data"), SandboxPath: filepath.Join(home, "data")},
				{HostPath: home, SandboxPath: home},
			},
		},
		{
			name: "No expansion",
			input: []sandbox.Mount{
				{HostPath: "/absolute/path"},
			},
			expected: []sandbox.Mount{
				{HostPath: "/absolute/path", SandboxPath: "/absolute/path"},
			},
		},
		{
			name: "Sandbox path tilde expansion",
			input: []sandbox.Mount{
				{HostPath: "/tmp", SandboxPath: "~/.config"},
				{HostPath: "/tmp", SandboxPath: "~/data"},
				{HostPath: "/tmp", SandboxPath: "~"},
			},
			expected: []sandbox.Mount{
				{HostPath: "/tmp", SandboxPath: "/root/.config"},
				{HostPath: "/tmp", SandboxPath: "/root/data"},
				{HostPath: "/tmp", SandboxPath: "/root"},
			},
		},
		{
			name:      "Cache directory expansion",
			input:     []sandbox.Mount{{HostPath: "${cacheDir}/pycache"}, {HostPath: "{cacheDir}/oldcache"}},
			imageSHA:  "abc",
			scriptKey: "key1",
		},
		{
			name:        "Rule 2: cache path escaping cache directory rejected",
			input:       []sandbox.Mount{{HostPath: "${cacheDir}/../escape"}},
			imageSHA:    "abc",
			scriptKey:   "key1",
			wantErr:     true,
			errContains: "${cacheDir}/../escape",
		},
		{
			name:      "Redundant .. inside cache directory accepted and cleaned",
			input:     []sandbox.Mount{{HostPath: "${cacheDir}/sub/../clean"}},
			imageSHA:  "abc",
			scriptKey: "key1",
			expected: []sandbox.Mount{
				{
					HostPath:    filepath.Join(userCache, "clix", "cache", "abc", "key1", "clean"),
					SandboxPath: filepath.Join(userCache, "clix", "cache", "abc", "key1", "clean"),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveMounts(tt.input, tt.imageSHA, tt.scriptKey)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveMounts succeeded, expected error")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got: %v", tt.errContains, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMounts failed: %v", err)
			}

			if tt.name == "Cache directory expansion" {
				expectedPathPart := filepath.Join("abc", "key1")
				for _, m := range got {
					if strings.Contains(m.HostPath, "cacheDir") {
						t.Errorf("Expected cacheDir to be expanded, got %q", m.HostPath)
					}
					if !strings.Contains(m.HostPath, expectedPathPart) {
						t.Errorf("Expected host path to contain %q, got %q", expectedPathPart, m.HostPath)
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
				if m.ReadOnly != tt.expected[i].ReadOnly {
					t.Errorf("mount[%d].ReadOnly = %v, want %v", i, m.ReadOnly, tt.expected[i].ReadOnly)
				}
			}
		})
	}

	t.Run("git.repoRoot(cwd)", func(t *testing.T) {
		repoDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(repoDir, ".git"), 0755); err != nil {
			t.Fatalf("failed to create .git: %v", err)
		}
		t.Chdir(repoDir)

		t.Run("repo-root mount yields root followed by read-only .git", func(t *testing.T) {
			got, err := resolveMounts([]sandbox.Mount{
				{HostPath: "git.repoRoot(cwd)"},
			}, "", "")
			if err != nil {
				t.Fatalf("resolveMounts failed: %v", err)
			}
			expected := []sandbox.Mount{
				{HostPath: repoDir, SandboxPath: repoDir, ReadOnly: false},
				{HostPath: filepath.Join(repoDir, ".git"), SandboxPath: filepath.Join(repoDir, ".git"), ReadOnly: true, WritableFallback: true},
			}
			if len(got) != len(expected) {
				t.Fatalf("expected %d mounts, got %d", len(expected), len(got))
			}
			for i, m := range got {
				if m != expected[i] {
					t.Errorf("mount[%d] = %+v, want %+v", i, m, expected[i])
				}
			}
		})

		t.Run("custom sandboxPath places .git under it", func(t *testing.T) {
			got, err := resolveMounts([]sandbox.Mount{
				{HostPath: "git.repoRoot(cwd)", SandboxPath: "/custom/sandbox"},
			}, "", "")
			if err != nil {
				t.Fatalf("resolveMounts failed: %v", err)
			}
			expected := []sandbox.Mount{
				{HostPath: repoDir, SandboxPath: "/custom/sandbox", ReadOnly: false},
				{HostPath: filepath.Join(repoDir, ".git"), SandboxPath: "/custom/sandbox/.git", ReadOnly: true, WritableFallback: true},
			}
			if len(got) != len(expected) {
				t.Fatalf("expected %d mounts, got %d", len(expected), len(got))
			}
			for i, m := range got {
				if m != expected[i] {
					t.Errorf("mount[%d] = %+v, want %+v", i, m, expected[i])
				}
			}
		})

		t.Run(".git as a file", func(t *testing.T) {
			worktreeDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte("gitdir: /path/to/main/.git/worktrees/wt\n"), 0644); err != nil {
				t.Fatalf("failed to create .git file: %v", err)
			}
			t.Chdir(worktreeDir)

			got, err := resolveMounts([]sandbox.Mount{
				{HostPath: "git.repoRoot(cwd)"},
			}, "", "")
			if err != nil {
				t.Fatalf("resolveMounts failed: %v", err)
			}
			expected := []sandbox.Mount{
				{HostPath: worktreeDir, SandboxPath: worktreeDir, ReadOnly: false},
				{HostPath: filepath.Join(worktreeDir, ".git"), SandboxPath: filepath.Join(worktreeDir, ".git"), ReadOnly: true, WritableFallback: true},
			}
			if len(got) != len(expected) {
				t.Fatalf("expected %d mounts, got %d", len(expected), len(got))
			}
			for i, m := range got {
				if m != expected[i] {
					t.Errorf("mount[%d] = %+v, want %+v", i, m, expected[i])
				}
			}
		})
	})

	t.Run("Key isolation and repeatability", func(t *testing.T) {
		mounts := []sandbox.Mount{{HostPath: "${cacheDir}/data"}}
		resKey1A, err := resolveMounts(mounts, "abc", "key1")
		if err != nil {
			t.Fatalf("resolveMounts failed: %v", err)
		}
		resKey1B, err := resolveMounts(mounts, "abc", "key1")
		if err != nil {
			t.Fatalf("resolveMounts failed: %v", err)
		}
		resKey2, err := resolveMounts(mounts, "abc", "key2")
		if err != nil {
			t.Fatalf("resolveMounts failed: %v", err)
		}

		if resKey1A[0].HostPath != resKey1B[0].HostPath {
			t.Errorf("expected same key to resolve to same directory, got %q and %q", resKey1A[0].HostPath, resKey1B[0].HostPath)
		}
		if resKey1A[0].HostPath == resKey2[0].HostPath {
			t.Errorf("expected different keys with same image SHA to resolve to different directories, both got %q", resKey1A[0].HostPath)
		}
	})
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
	// We expect clix-test-script-<hash>:0123456789abcdef0123456789abcdef01234567
	if !strings.HasPrefix(imageTag, "clix-test-script-") {
		t.Errorf("Unexpected image tag prefix: %s", imageTag)
	}
	if !strings.HasSuffix(imageTag, ":0123456789abcdef0123456789abcdef01234567") {
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

	// We expect clix-test-script-<hash>:0123456789abcdef0123456789abcdef01234567
	if !strings.HasPrefix(imageTag, "clix-test-script-") {

		t.Errorf("Unexpected image tag: %s", imageTag)

	}

}

func fakeLsRemoteOutput(output string, recorded *[][]string) func(string, ...string) *exec.Cmd {
	return func(name string, args ...string) *exec.Cmd {
		if recorded != nil {
			*recorded = append(*recorded, append([]string{name}, args...))
		}
		cs := []string{"-test.run=TestHelperLsRemoteProcess", "--"}
		cs = append(cs, args...)
		cmd := exec.Command(os.Args[0], cs...)
		cmd.Env = append(os.Environ(), "GO_WANT_LS_REMOTE_HELPER_PROCESS=1", "LS_REMOTE_OUTPUT="+output)
		return cmd
	}
}

func TestHelperLsRemoteProcess(t *testing.T) {
	if os.Getenv("GO_WANT_LS_REMOTE_HELPER_PROCESS") != "1" {
		return
	}
	fmt.Print(os.Getenv("LS_REMOTE_OUTPUT"))
	os.Exit(0)
}

func TestGetRemoteHead(t *testing.T) {
	tests := []struct {
		name         string
		branch       string
		cannedOutput string
		wantHash     string
		wantErr      bool
		errContains  string
	}{
		{
			name:         "both a/main and main present with a/main first",
			branch:       "main",
			cannedOutput: "1111111111111111111111111111111111111111\trefs/heads/a/main\n2222222222222222222222222222222222222222\trefs/heads/main\n",
			wantHash:     "2222222222222222222222222222222222222222",
		},
		{
			name:         "both a/main and main present with main first",
			branch:       "main",
			cannedOutput: "2222222222222222222222222222222222222222\trefs/heads/main\n1111111111111111111111111111111111111111\trefs/heads/a/main\n",
			wantHash:     "2222222222222222222222222222222222222222",
		},
		{
			name:         "tag only",
			branch:       "v1.0.0",
			cannedOutput: "3333333333333333333333333333333333333333\trefs/tags/v1.0.0\n",
			wantHash:     "3333333333333333333333333333333333333333",
		},
		{
			name:         "both branch and tag present, tag first, branch preferred",
			branch:       "release",
			cannedOutput: "4444444444444444444444444444444444444444\trefs/tags/release\n5555555555555555555555555555555555555555\trefs/heads/release\n",
			wantHash:     "5555555555555555555555555555555555555555",
		},
		{
			name:         "both branch and tag present, branch first, branch preferred",
			branch:       "release",
			cannedOutput: "5555555555555555555555555555555555555555\trefs/heads/release\n4444444444444444444444444444444444444444\trefs/tags/release\n",
			wantHash:     "5555555555555555555555555555555555555555",
		},
		{
			name:         "no match with different branch",
			branch:       "main",
			cannedOutput: "1111111111111111111111111111111111111111\trefs/heads/other\n",
			wantErr:      true,
			errContains:  "main",
		},
		{
			name:         "no match with suffix only (a/main)",
			branch:       "main",
			cannedOutput: "1111111111111111111111111111111111111111\trefs/heads/a/main\n",
			wantErr:      true,
			errContains:  "main",
		},
		{
			name:         "no match with empty output",
			branch:       "main",
			cannedOutput: "",
			wantErr:      true,
			errContains:  "main",
		},
		{
			name:         "non-hex first field",
			branch:       "main",
			cannedOutput: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz\trefs/heads/main\n",
			wantErr:      true,
		},
		{
			name:         "hash too short (16 hex chars)",
			branch:       "main",
			cannedOutput: "abcdef1234567890\trefs/heads/main\n",
			wantErr:      true,
		},
		{
			name:         "hash too long (41 hex chars)",
			branch:       "main",
			cannedOutput: "0123456789abcdef0123456789abcdef012345678\trefs/heads/main\n",
			wantErr:      true,
		},
		{
			name:         "64-character hex hash (SHA-256)",
			branch:       "main",
			cannedOutput: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\trefs/heads/main\n",
			wantHash:     "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldExec := execCommand
			defer func() { execCommand = oldExec }()

			var recorded [][]string
			execCommand = fakeLsRemoteOutput(tc.cannedOutput, &recorded)

			got, err := getRemoteHead("https://example.com/repo", tc.branch)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (hash=%q)", got)
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Errorf("expected error containing %q, got: %v", tc.errContains, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantHash {
				t.Errorf("got hash %q, want %q", got, tc.wantHash)
			}

			if len(recorded) != 1 {
				t.Fatalf("expected 1 command executed, got %d", len(recorded))
			}
			cmd := recorded[0]
			expectedArgs := []string{"git", "ls-remote", "--", "https://example.com/repo", "refs/heads/" + tc.branch, "refs/tags/" + tc.branch}
			if len(cmd) != len(expectedArgs) {
				t.Fatalf("expected command args %v, got %v", expectedArgs, cmd)
			}
			for i := range expectedArgs {
				if cmd[i] != expectedArgs[i] {
					t.Errorf("arg %d: expected %q, got %q", i, expectedArgs[i], cmd[i])
				}
			}
		})
	}

	t.Run("empty branch resolves HEAD", func(t *testing.T) {
		oldExec := execCommand
		defer func() { execCommand = oldExec }()

		var recorded [][]string
		cannedOutput := "2222222222222222222222222222222222222222\tHEAD\n"
		execCommand = fakeLsRemoteOutput(cannedOutput, &recorded)

		got, err := getRemoteHead("https://example.com/repo", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "2222222222222222222222222222222222222222" {
			t.Errorf("got hash %q, want 2222222222222222222222222222222222222222", got)
		}
		if len(recorded) != 1 {
			t.Fatalf("expected 1 command, got %d", len(recorded))
		}
		expectedArgs := []string{"git", "ls-remote", "--", "https://example.com/repo", "HEAD"}
		if len(recorded[0]) != len(expectedArgs) {
			t.Fatalf("expected args %v, got %v", expectedArgs, recorded[0])
		}
		for i := range expectedArgs {
			if recorded[0][i] != expectedArgs[i] {
				t.Errorf("arg %d: expected %q, got %q", i, expectedArgs[i], recorded[0][i])
			}
		}
	})
}

type recordingSandbox struct {
	configs []sandbox.Config
}

func (s *recordingSandbox) Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, script sandbox.Config, args []string) error {
	s.configs = append(s.configs, script)
	return nil
}

func TestRunCacheKeyPerScript(t *testing.T) {
	t.Setenv("CLIX_SANDBOX", "docker")

	tmpDir := t.TempDir()
	script1Path := filepath.Join(tmpDir, "script1.yaml")
	script2Path := filepath.Join(tmpDir, "script2.yaml")

	script1Content := `#!/usr/bin/env clix
image: alpine
mounts:
  - hostPath: ${cacheDir}/data
    sandboxPath: /data
`
	script2Content := `#!/usr/bin/env clix
image: alpine
mounts:
  - hostPath: ${cacheDir}/data
    sandboxPath: /data
`
	if err := os.WriteFile(script1Path, []byte(script1Content), 0755); err != nil {
		t.Fatalf("Failed to write script1: %v", err)
	}
	if err := os.WriteFile(script2Path, []byte(script2Content), 0755); err != nil {
		t.Fatalf("Failed to write script2: %v", err)
	}

	fake := &recordingSandbox{}
	oldNewSandbox := newSandbox
	defer func() { newSandbox = oldNewSandbox }()
	newSandbox = func(sandboxType string) (sandbox.Sandbox, string, error) {
		return fake, "docker", nil
	}

	oldGetImageSHA := getImageSHAFunction
	defer func() { getImageSHAFunction = oldGetImageSHA }()
	getImageSHAFunction = func(string) (string, error) {
		return "mocksha256", nil
	}

	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader("")

	if err := run(stdin, &stdout, &stderr, []string{"clix", script1Path}); err != nil {
		t.Fatalf("run script1 failed: %v", err)
	}
	if err := run(stdin, &stdout, &stderr, []string{"clix", script2Path}); err != nil {
		t.Fatalf("run script2 failed: %v", err)
	}

	if len(fake.configs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(fake.configs))
	}

	findCacheMount := func(c sandbox.Config) string {
		for _, m := range c.Mounts {
			if m.SandboxPath == "/data" {
				return m.HostPath
			}
		}
		return ""
	}

	hostPath1 := findCacheMount(fake.configs[0])
	hostPath2 := findCacheMount(fake.configs[1])

	if hostPath1 == "" {
		t.Fatalf("failed to find cache mount in script1: %+v", fake.configs[0].Mounts)
	}
	if hostPath2 == "" {
		t.Fatalf("failed to find cache mount in script2: %+v", fake.configs[1].Mounts)
	}
	if hostPath1 == hostPath2 {
		t.Errorf("expected different host cache paths for two scripts, got %q for both", hostPath1)
	}

	absPath1, err := filepath.Abs(script1Path)
	if err != nil {
		t.Fatalf("filepath.Abs failed: %v", err)
	}
	h1 := sha256.Sum256([]byte(absPath1))
	expectedKey1 := hex.EncodeToString(h1[:])[:16]

	absPath2, err := filepath.Abs(script2Path)
	if err != nil {
		t.Fatalf("filepath.Abs failed: %v", err)
	}
	h2 := sha256.Sum256([]byte(absPath2))
	expectedKey2 := hex.EncodeToString(h2[:])[:16]

	if !strings.Contains(hostPath1, expectedKey1) {
		t.Errorf("expected hostPath1 to contain script key %q, got %q", expectedKey1, hostPath1)
	}
	if !strings.Contains(hostPath2, expectedKey2) {
		t.Errorf("expected hostPath2 to contain script key %q, got %q", expectedKey2, hostPath2)
	}
}

func TestHelperDockerProcess(t *testing.T) {
	if os.Getenv("GO_WANT_DOCKER_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		args = args[1:]
	}
	if len(args) >= 2 && args[0] == "docker" && args[1] == "images" {
		fmt.Println("mocksha256")
		os.Exit(0)
	}
	os.Exit(0)
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
		wantReadOnly  bool
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
			wantReadOnly: true,
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
			wantReadOnly: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &recordingSandbox{}
			oldNewSandbox := newSandbox
			defer func() { newSandbox = oldNewSandbox }()
			newSandbox = func(sandboxType string) (sandbox.Sandbox, string, error) {
				return fake, "docker", nil
			}

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

			if len(fake.configs) != 1 {
				t.Fatalf("expected 1 run, got %d", len(fake.configs))
			}
			foundMount := false
			for _, m := range fake.configs[0].Mounts {
				if m.HostPath == "/tmp/host" && m.SandboxPath == "/tmp/sandbox" {
					if m.ReadOnly != tt.wantReadOnly {
						t.Errorf("expected mount ReadOnly=%v, got %v", tt.wantReadOnly, m.ReadOnly)
					}
					foundMount = true
					break
				}
			}
			if !foundMount {
				t.Errorf("expected mount /tmp/host:/tmp/sandbox in %+v", fake.configs[0].Mounts)
			}
		})
	}
}

func TestBackendExitError(t *testing.T) {
	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand
	t.Setenv("MOCK_BEHAVIOR", "exit_42")

	t.Run("runGo", func(t *testing.T) {
		config := &GoConfig{Run: "example.com/tool"}
		var stdout, stderr bytes.Buffer
		err := runGo(strings.NewReader(""), &stdout, &stderr, config, []string{"exit42"})
		if err == nil {
			t.Fatalf("expected error from failing tool, got nil")
		}

		var exitError *sandbox.ExitError
		if !errors.As(err, &exitError) {
			t.Fatalf("expected *ExitError, got %T: %v", err, err)
		}
		if exitError.ExitCode() != 42 {
			t.Errorf("expected exit code 42, got %d", exitError.ExitCode())
		}
	})
}

func TestMainExitStatus(t *testing.T) {
	temporaryDirectory := t.TempDir()
	runClix := func(t *testing.T, scriptContent, mockBehavior string) (int, string) {
		t.Helper()
		scriptPath := filepath.Join(temporaryDirectory, "script.yaml")
		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("failed to write script: %v", err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=TestHelperMainProcess", "--", scriptPath)
		cmd.Env = append(os.Environ(), "GO_WANT_MAIN_HELPER_PROCESS=1", "MOCK_BEHAVIOR="+mockBehavior)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err == nil {
			return 0, stderr.String()
		}
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return exitError.ExitCode(), stderr.String()
		}
		t.Fatalf("unexpected error running main helper: %v", err)
		return -1, ""
	}

	t.Run("ToolExitPassedThroughSilently", func(t *testing.T) {
		exitCode, stderr := runClix(t, "#!/usr/bin/env clix\nimage: mock-image\n", "exit_42")
		if exitCode != 42 {
			t.Errorf("expected exit code 42, got %d", exitCode)
		}
		if stderr != "" {
			t.Errorf("expected empty stderr, got: %q", stderr)
		}
	})

	t.Run("HelperFailureProducesErrorMessage", func(t *testing.T) {
		exitCode, stderr := runClix(t, "#!/usr/bin/env clix\nbuild:\n  git: https://github.com/example/repo\n", "fail_git_clone")
		if exitCode != 1 {
			t.Errorf("expected exit code 1, got %d", exitCode)
		}
		if !strings.Contains(stderr, "git clone failed") {
			t.Errorf("expected stderr to contain 'git clone failed', got: %q", stderr)
		}
	})
}

func TestCacheDirPerBackend(t *testing.T) {
	userCache, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("failed to get user cache dir: %v", err)
	}

	oldDockerSHA, oldAppleSHA, oldProotSHA := getImageSHAFunction, getAppleContainerImageSHAFunction, getProotImageSHAFunction
	defer func() {
		getImageSHAFunction, getAppleContainerImageSHAFunction, getProotImageSHAFunction = oldDockerSHA, oldAppleSHA, oldProotSHA
	}()
	getImageSHAFunction = func(string) (string, error) { return "dockersha123", nil }
	getAppleContainerImageSHAFunction = func(string) (string, error) { return "applesha123", nil }
	getProotImageSHAFunction = func(string) (string, error) { return "prootsha123", nil }

	for _, syntax := range []struct {
		name, hostPath string
	}{
		{"deprecated {cacheDir}", "{cacheDir}/python"},
		{"standard ${cacheDir}", "${cacheDir}/python"},
	} {
		for _, testCase := range []struct {
			backend, sha string
		}{
			{"docker", "dockersha123"},
			{"apple-container", "applesha123"},
			{"proot", "prootsha123"},
		} {
			t.Run(testCase.backend+"_"+syntax.name, func(t *testing.T) {
				localGetImageSHA := getImageSHAFunction
				defer func() { getImageSHAFunction = localGetImageSHA }()
				if testCase.backend == "proot" {
					getImageSHAFunction = func(string) (string, error) {
						t.Fatal("proot must not depend on docker for image digest")
						return "", nil
					}
				}
				mounts := []sandbox.Mount{{HostPath: syntax.hostPath, SandboxPath: "/tmp/.clix-pycache"}}
				resolved, err := resolveScriptMounts(mounts, testCase.backend, "test-image:v1", "key123456")
				if err != nil {
					t.Fatalf("resolveScriptMounts failed: %v", err)
				}
				expectedPath := filepath.Join(userCache, "clix", "cache", testCase.sha, "key123456", "python")
				if resolved[0].HostPath != expectedPath {
					t.Errorf("expected %q, got %q", expectedPath, resolved[0].HostPath)
				}
			})
		}
	}
}

func TestClixLogSink(t *testing.T) {
	t.Run("InfoWithKeyValuePairs", func(t *testing.T) {
		var output bytes.Buffer
		sink := &clixLogSink{writer: &output, verbosity: 1}
		logr.New(sink).Info("preparing sandbox", "directory", "/tmp/sandbox-1")
		expectedOutput := "clix: preparing sandbox directory=/tmp/sandbox-1\n"
		if actualOutput := output.String(); actualOutput != expectedOutput {
			t.Errorf("got %q, want %q", actualOutput, expectedOutput)
		}
	})

	t.Run("ErrorWithKeyValuePairs", func(t *testing.T) {
		var output bytes.Buffer
		sink := &clixLogSink{writer: &output, verbosity: 1}
		logr.New(sink).Error(errors.New("permission denied"), "failed to clean up", "directory", "/tmp/sandbox-2")
		expectedOutput := "clix: failed to clean up: permission denied directory=/tmp/sandbox-2\n"
		if actualOutput := output.String(); actualOutput != expectedOutput {
			t.Errorf("got %q, want %q", actualOutput, expectedOutput)
		}
	})

	t.Run("WithValuesAccumulatesPairs", func(t *testing.T) {
		var output bytes.Buffer
		sink := &clixLogSink{writer: &output, verbosity: 1}
		logr.New(sink).WithValues("component", "sandbox").Info("running tool", "tool", "echo")
		expectedOutput := "clix: running tool component=sandbox tool=echo\n"
		if actualOutput := output.String(); actualOutput != expectedOutput {
			t.Errorf("got %q, want %q", actualOutput, expectedOutput)
		}
	})

	t.Run("LogFunctionRoutesThroughLogger", func(t *testing.T) {
		var output bytes.Buffer
		oldLogger := defaultLogger
		defer func() { defaultLogger = oldLogger }()
		defaultLogger = logr.New(&clixLogSink{writer: &output, verbosity: 1})

		log(1, "using sandbox: %s", "docker")
		expectedOutput := "clix: using sandbox: docker\n"
		if actualOutput := output.String(); actualOutput != expectedOutput {
			t.Errorf("got %q, want %q", actualOutput, expectedOutput)
		}
	})
}

func TestUnrecognisedSandboxBackend(t *testing.T) {
	t.Setenv("CLIX_SANDBOX", "bogus")
	temporaryDirectory := t.TempDir()
	scriptPath := filepath.Join(temporaryDirectory, "script.yaml")
	scriptContent := `#!/usr/bin/env clix
image: alpine
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err := run(strings.NewReader(""), &stdout, &stderr, []string{"clix", scriptPath})
	if err == nil {
		t.Fatal("expected error for unrecognised sandbox backend, got nil")
	}
	expectedErrorMessage := `unknown sandbox backend "bogus"`
	if !strings.Contains(err.Error(), expectedErrorMessage) {
		t.Errorf("got error %q, want it to contain %q", err.Error(), expectedErrorMessage)
	}
}

func TestRunNetworkNone(t *testing.T) {
	fake := &recordingSandbox{}
	oldNewSandbox := newSandbox
	defer func() { newSandbox = oldNewSandbox }()
	newSandbox = func(sandboxType string) (sandbox.Sandbox, string, error) {
		return fake, "docker", nil
	}

	temporaryDirectory := t.TempDir()
	scriptPath := filepath.Join(temporaryDirectory, "script.yaml")
	scriptContent := `#!/usr/bin/env clix
image: alpine
network: none
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := run(strings.NewReader(""), &stdout, &stderr, []string{"clix", scriptPath}); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if len(fake.configs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(fake.configs))
	}
	if fake.configs[0].Network != "none" {
		t.Errorf("expected Network to be %q, got %q", "none", fake.configs[0].Network)
	}
}

func TestRunBadMounts(t *testing.T) {
	tests := []struct {
		name            string
		script          string
		expectedMessage string
	}{
		{
			name: "relative host path",
			script: `#!/usr/bin/env clix
image: alpine
mounts:
- hostPath: relative/path
  sandboxPath: /tmp
`,
			expectedMessage: `mount "relative/path": host path is not absolute`,
		},
		{
			name: "relative sandbox path",
			script: `#!/usr/bin/env clix
image: alpine
mounts:
- hostPath: /tmp
  sandboxPath: relative/sandbox
`,
			expectedMessage: `mount "/tmp": sandbox path "relative/sandbox" is not absolute`,
		},
		{
			name: "colon in host path",
			script: `#!/usr/bin/env clix
image: alpine
mounts:
- hostPath: /tmp/has:colon
  sandboxPath: /tmp
`,
			expectedMessage: `mount "/tmp/has:colon": host path contains ':'`,
		},
		{
			name: "colon in sandbox path",
			script: `#!/usr/bin/env clix
image: alpine
mounts:
- hostPath: /tmp
  sandboxPath: /sandbox:colon
`,
			expectedMessage: `mount "/tmp": sandbox path "/sandbox:colon" contains ':'`,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			temporaryDirectory := t.TempDir()
			scriptPath := filepath.Join(temporaryDirectory, "script.yaml")
			if err := os.WriteFile(scriptPath, []byte(testCase.script), 0755); err != nil {
				t.Fatalf("failed to write script: %v", err)
			}

			var stdout, stderr bytes.Buffer
			err := run(strings.NewReader(""), &stdout, &stderr, []string{"clix", scriptPath})
			if err == nil {
				t.Fatalf("expected error running script with bad mount, got nil")
			}
			if !strings.Contains(err.Error(), testCase.expectedMessage) {
				t.Errorf("expected error containing %q, got: %v", testCase.expectedMessage, err)
			}
		})
	}
}
