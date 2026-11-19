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
	"errors"
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

func TestChrootUsable(t *testing.T) {
	sandbox := &chrootSandbox{}

	t.Run("ExpectedCause", func(t *testing.T) {
		testCases := []struct {
			name           string
			simulatedError error
		}{
			{name: "PermissionDenied", simulatedError: syscall.EPERM},
			{name: "InvalidArgument", simulatedError: syscall.EINVAL},
			{name: "NoSpaceLeft", simulatedError: syscall.ENOSPC},
			{name: "TooManyUsers", simulatedError: syscall.EUSERS},
		}

		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				oldRunCommand := runCommandFunction
				defer func() { runCommandFunction = oldRunCommand }()
				runCommandFunction = func(ctx context.Context, backendName string, cmd *exec.Cmd, stopAction func() error) error {
					return testCase.simulatedError
				}

				err := sandbox.Usable(t.Context())
				if err == nil {
					t.Fatalf("expected error from Usable, got nil")
				}

				expectedMessage := "unprivileged user namespaces are not available for chroot: enable kernel user namespaces"
				if !strings.Contains(err.Error(), expectedMessage) {
					t.Errorf("expected error to contain %q, got %q", expectedMessage, err.Error())
				}
				if !errors.Is(err, testCase.simulatedError) {
					t.Errorf("expected errors.Is(err, %v), got %v", testCase.simulatedError, err)
				}
			})
		}
	})

	t.Run("UnrelatedFailure", func(t *testing.T) {
		testCases := []struct {
			name           string
			simulatedError error
		}{
			{name: "AccessDenied", simulatedError: syscall.EACCES},
			{name: "ResourceUnavailable", simulatedError: syscall.EAGAIN},
		}

		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				oldRunCommand := runCommandFunction
				defer func() { runCommandFunction = oldRunCommand }()
				runCommandFunction = func(ctx context.Context, backendName string, cmd *exec.Cmd, stopAction func() error) error {
					return testCase.simulatedError
				}

				err := sandbox.Usable(t.Context())
				if err == nil {
					t.Fatalf("expected error from Usable, got nil")
				}

				unexpectedMessage := "unprivileged user namespaces are not available for chroot"
				if strings.Contains(err.Error(), unexpectedMessage) {
					t.Errorf("expected error not to assume user namespaces cause %q, got %q", unexpectedMessage, err.Error())
				}

				expectedPrefix := "cannot probe user namespaces"
				if !strings.Contains(err.Error(), expectedPrefix) {
					t.Errorf("expected error to contain %q, got %q", expectedPrefix, err.Error())
				}
				if !errors.Is(err, testCase.simulatedError) {
					t.Errorf("expected errors.Is(err, %v), got %v", testCase.simulatedError, err)
				}
			})
		}
	})
}
