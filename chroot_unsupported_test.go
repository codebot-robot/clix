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

//go:build !linux

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunChrootUnsupported(t *testing.T) {
	os.Setenv("CLIX_SANDBOX", "chroot")
	defer os.Unsetenv("CLIX_SANDBOX")

	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "test-script-chroot")
	scriptContent := `#!/usr/bin/env clix
image: hello-world
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	var stdoutBuffer, stderrBuffer bytes.Buffer
	stdin := strings.NewReader("")
	args := []string{"clix", scriptPath}

	err := run(stdin, &stdoutBuffer, &stderrBuffer, args)
	if err == nil {
		t.Fatalf("expected error selecting chroot on unsupported platform, got nil")
	}
	expected := fmt.Sprintf("chroot sandbox is not supported on %s", runtime.GOOS)
	if !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}
