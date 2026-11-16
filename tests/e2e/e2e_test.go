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

package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2E(t *testing.T) {
	if os.Getenv("RUN_E2E") == "" {
		t.Skip("Skipping E2E test; set RUN_E2E=1 to run")
	}

	// Get repo root using git rev-parse --show-toplevel
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("failed to get repo root: %v", err)
	}
	repoRoot := strings.TrimSpace(string(out))

	// Build clix binary
	tempDir := t.TempDir()
	buildCmd := exec.Command("go", "build", "-o", filepath.Join(tempDir, "clix"), ".")
	buildCmd.Dir = repoRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build clix: %v\nOutput: %s", err, out)
	}

	// Run example.
	shfmtExample := filepath.Join(repoRoot, "examples", "shfmt")
	runCmd := exec.Command(shfmtExample, "--version")
	runCmd.Env = append(os.Environ(), "PATH="+tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := runCmd.CombinedOutput(); err != nil {
		// In some environments, this might fail due to Docker rate limits.
		// We log the error but it might cause the test to fail.
		t.Errorf("shfmt --version failed: %v\nOutput: %s", err, out)
	} else {
		t.Logf("shfmt --version succeeded: %s", out)
	}

	t.Run("NetworkNone", func(t *testing.T) {
		isolatedScript := filepath.Join(tempDir, "isolated-script")
		isolatedContent := "#!/usr/bin/env clix\nimage: alpine:latest\nnetwork: none\n"
		if err := os.WriteFile(isolatedScript, []byte(isolatedContent), 0755); err != nil {
			t.Fatalf("failed to write script: %v", err)
		}
		var isolatedStderr bytes.Buffer
		cmd := exec.Command(isolatedScript, "ls", "/sys/class/net")
		cmd.Env = append(os.Environ(), "PATH="+tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		cmd.Stderr = &isolatedStderr
		isolatedStdout, err := cmd.Output()
		if err != nil {
			t.Fatalf("isolated run failed: %v\nStderr: %s", err, isolatedStderr.String())
		}
		if interfaces := strings.TrimSpace(string(isolatedStdout)); interfaces != "lo" {
			t.Errorf("expected only loopback interface 'lo', got: %q", interfaces)
		}

		defaultScript := filepath.Join(tempDir, "default-script")
		defaultContent := "#!/usr/bin/env clix\nimage: alpine:latest\n"
		if err := os.WriteFile(defaultScript, []byte(defaultContent), 0755); err != nil {
			t.Fatalf("failed to write script: %v", err)
		}
		var defaultStderr bytes.Buffer
		defaultCmd := exec.Command(defaultScript, "ls", "/sys/class/net")
		defaultCmd.Env = append(os.Environ(), "PATH="+tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		defaultCmd.Stderr = &defaultStderr
		defaultStdout, err := defaultCmd.Output()
		if err != nil {
			t.Fatalf("default run failed: %v\nStderr: %s", err, defaultStderr.String())
		}
		if interfaces := strings.TrimSpace(string(defaultStdout)); interfaces == "lo" {
			t.Errorf("expected default run to have network interfaces beyond 'lo', got: %q", interfaces)
		}
	})
}
