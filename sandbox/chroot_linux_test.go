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

//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func TestChrootNetworkConfiguration(t *testing.T) {
	oldPrepareRootFS := prepareRootFSFunction
	defer func() { prepareRootFSFunction = oldPrepareRootFS }()
	prepareRootFSFunction = func(ctx context.Context, imageRef string) (*preparedRootFS, error) {
		return &preparedRootFS{
			dir:        t.TempDir(),
			entrypoint: []string{"/bin/sh"},
			cleanup:    func() {},
		}, nil
	}

	testCases := []struct {
		name                   string
		network                string
		expectNetworkNamespace bool
	}{
		{
			name:                   "DefaultNetwork",
			network:                "",
			expectNetworkNamespace: false,
		},
		{
			name:                   "NetworkNone",
			network:                "none",
			expectNetworkNamespace: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			oldRunCommand := runCommandFunction
			defer func() { runCommandFunction = oldRunCommand }()
			var capturedCommand *exec.Cmd
			runCommandFunction = func(ctx context.Context, backendName string, cmd *exec.Cmd, stopAction func() error) error {
				capturedCommand = cmd
				return nil
			}

			sandbox := &chrootSandbox{}
			script := Config{
				Image:   "test-image",
				Network: testCase.network,
			}
			var stdout, stderr bytes.Buffer
			if err := sandbox.Run(t.Context(), script, nil, RunOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}); err != nil {
				t.Fatalf("sandbox.Run failed: %v", err)
			}
			if capturedCommand == nil || capturedCommand.SysProcAttr == nil {
				t.Fatalf("expected captured command with SysProcAttr")
			}
			hasNetworkNamespace := capturedCommand.SysProcAttr.Cloneflags&syscall.CLONE_NEWNET != 0
			if hasNetworkNamespace != testCase.expectNetworkNamespace {
				t.Errorf("got Cloneflags CLONE_NEWNET=%v, want %v (Cloneflags=0x%x)", hasNetworkNamespace, testCase.expectNetworkNamespace, capturedCommand.SysProcAttr.Cloneflags)
			}
		})
	}
}
