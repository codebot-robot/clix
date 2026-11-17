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

//go:build unix

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func isProcessRunning(processID int) bool {
	err := syscall.Kill(processID, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false
	}
	data, readError := os.ReadFile(fmt.Sprintf("/proc/%d/status", processID))
	if readError != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "State:") {
			return !strings.Contains(line, "Z (zombie)")
		}
	}
	return true
}

func TestProcessGroupGrandchildKilledOnCancel(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process group grandchild test relies on Linux procfs")
	}

	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand

	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	t.Setenv("GRANDCHILD_PID_FILE", pidFile)
	t.Setenv("MOCK_BEHAVIOR", "spawn_grandchild")

	backend := &prootSandbox{}
	oldPrepareRootFSFunction := prepareRootFSFunction
	defer func() { prepareRootFSFunction = oldPrepareRootFSFunction }()
	prepareRootFSFunction = func(ctx context.Context, imageRef string) (*preparedRootFS, error) {
		return &preparedRootFS{
			dir:     t.TempDir(),
			cleanup: func() {},
		}, nil
	}

	config := Config{
		Image:      "alpine",
		Entrypoint: "/bin/sleep",
		Env: []EnvVar{
			{Name: "GO_WANT_HELPER_PROCESS", Value: "1"},
			{Name: "MOCK_BEHAVIOR", Value: "spawn_grandchild"},
			{Name: "GRANDCHILD_PID_FILE", Value: pidFile},
		},
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		runDone <- backend.Run(ctx, strings.NewReader(""), &stdout, &stderr, config, []string{"sleep"})
	}()

	var grandchildProcessID int
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(pidFile)
		if err == nil && len(data) > 0 {
			if parsed, parseError := strconv.Atoi(strings.TrimSpace(string(data))); parseError == nil {
				grandchildProcessID = parsed
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for grandchild process ID file")
		}
		time.Sleep(20 * time.Millisecond)
	}

	defer func() {
		if err := syscall.Kill(grandchildProcessID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Logf("cleaning up grandchild %d: %v", grandchildProcessID, err)
		}
	}()

	if !isProcessRunning(grandchildProcessID) {
		t.Fatalf("expected grandchild process %d to be running before cancellation", grandchildProcessID)
	}

	cancel()

	select {
	case err := <-runDone:
		if err == nil {
			t.Fatalf("expected error from cancelled Run, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected error wrapping context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return promptly after cancellation")
	}

	checkDeadline := time.Now().Add(2 * time.Second)
	for isProcessRunning(grandchildProcessID) {
		if time.Now().After(checkDeadline) {
			t.Fatalf("grandchild process %d is still running after process group cancellation", grandchildProcessID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
