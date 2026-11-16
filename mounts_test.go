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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindGitRoot(t *testing.T) {
	t.Run("nested working directory", func(t *testing.T) {
		repoDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(repoDir, ".git"), 0755); err != nil {
			t.Fatalf("failed to create .git: %v", err)
		}
		nestedDir := filepath.Join(repoDir, "a", "b", "c")
		if err := os.MkdirAll(nestedDir, 0755); err != nil {
			t.Fatalf("failed to create nested dirs: %v", err)
		}

		got, err := findGitRoot(nestedDir)
		if err != nil {
			t.Fatalf("findGitRoot failed: %v", err)
		}
		if got != repoDir {
			t.Errorf("findGitRoot(%q) = %q, want %q", nestedDir, got, repoDir)
		}
	})

	t.Run(".git as a file", func(t *testing.T) {
		repoDir := t.TempDir()
		gitFile := filepath.Join(repoDir, ".git")
		if err := os.WriteFile(gitFile, []byte("gitdir: /path/to/gitdir\n"), 0644); err != nil {
			t.Fatalf("failed to create .git file: %v", err)
		}
		nestedDir := filepath.Join(repoDir, "sub")
		if err := os.MkdirAll(nestedDir, 0755); err != nil {
			t.Fatalf("failed to create nested dir: %v", err)
		}

		got, err := findGitRoot(nestedDir)
		if err != nil {
			t.Fatalf("findGitRoot failed: %v", err)
		}
		if got != repoDir {
			t.Errorf("findGitRoot(%q) = %q, want %q", nestedDir, got, repoDir)
		}
	})

	t.Run("no repository", func(t *testing.T) {
		emptyDir := t.TempDir()
		got, err := findGitRoot(emptyDir)
		if err == nil {
			t.Fatalf("findGitRoot(%q) succeeded with %q, expected error", emptyDir, got)
		}
	})

	t.Run("core.worktree in .git/config still resolves to directory containing .git", func(t *testing.T) {
		repoDir := t.TempDir()
		gitDir := filepath.Join(repoDir, ".git")
		if err := os.Mkdir(gitDir, 0755); err != nil {
			t.Fatalf("failed to create .git: %v", err)
		}
		otherDir := t.TempDir()
		configContent := "[core]\n\tworktree = " + otherDir + "\n"
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write git config: %v", err)
		}

		got, err := findGitRoot(repoDir)
		if err != nil {
			t.Fatalf("findGitRoot failed: %v", err)
		}
		if got != repoDir {
			t.Errorf("findGitRoot(%q) = %q, want %q (should ignore core.worktree %q)", repoDir, got, repoDir, otherDir)
		}
	})

	t.Run("lstat error other than not exist is returned", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "somefile")
		if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}
		_, err := findGitRoot(filepath.Join(f, "child"))
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "somefile") {
			t.Errorf("expected error to mention somefile, got %v", err)
		}
	})
}
