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
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestFirstUsableDockerAnswers(t *testing.T) {
	oldLookPath := lookPathFunction
	defer func() { lookPathFunction = oldLookPath }()
	lookPathFunction = func(file string) (string, error) {
		if file == "docker" {
			return "/bin/docker", nil
		}
		return "", exec.ErrNotFound
	}

	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand

	backend, err := FirstUsable(t.Context())
	if err != nil {
		t.Fatalf("FirstUsable failed: %v", err)
	}
	if backend == nil {
		t.Fatal("expected non-nil backend")
	}
	if backend.String() != "docker" {
		t.Errorf("expected backend 'docker', got %q", backend.String())
	}
	if err := backend.Usable(t.Context()); err != nil {
		t.Errorf("expected backend to report usable, got %v", err)
	}
}

func TestFirstUsableDockerAbsentAnotherBackendUsable(t *testing.T) {
	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand

	t.Run("ChrootUsableWhenDockerAbsent", func(t *testing.T) {
		oldLookPath := lookPathFunction
		defer func() { lookPathFunction = oldLookPath }()
		lookPathFunction = func(file string) (string, error) {
			return "", exec.ErrNotFound
		}

		oldAppleOperatingSystem := appleContainerOperatingSystem
		defer func() { appleContainerOperatingSystem = oldAppleOperatingSystem }()
		appleContainerOperatingSystem = "linux"

		oldCheckUserNamespaces := checkUserNamespacesFunction
		defer func() { checkUserNamespacesFunction = oldCheckUserNamespaces }()
		checkUserNamespacesFunction = func(ctx context.Context) error {
			return nil
		}

		backend, err := FirstUsable(t.Context())
		if err != nil {
			t.Fatalf("FirstUsable failed: %v", err)
		}
		if backend.String() != "chroot" {
			t.Errorf("expected backend 'chroot', got %q", backend.String())
		}
	})

	t.Run("ProotUsableWhenDockerAndChrootUnavailable", func(t *testing.T) {
		oldLookPath := lookPathFunction
		defer func() { lookPathFunction = oldLookPath }()
		lookPathFunction = func(file string) (string, error) {
			if file == "proot" {
				return "/bin/proot", nil
			}
			return "", exec.ErrNotFound
		}

		oldAppleOperatingSystem := appleContainerOperatingSystem
		defer func() { appleContainerOperatingSystem = oldAppleOperatingSystem }()
		appleContainerOperatingSystem = "linux"

		oldCheckUserNamespaces := checkUserNamespacesFunction
		defer func() { checkUserNamespacesFunction = oldCheckUserNamespaces }()
		checkUserNamespacesFunction = func(ctx context.Context) error {
			return fmt.Errorf("unprivileged user namespaces are not available for chroot: enable kernel user namespaces")
		}

		oldProotOperatingSystem := prootOperatingSystem
		defer func() { prootOperatingSystem = oldProotOperatingSystem }()
		prootOperatingSystem = "linux"

		backend, err := FirstUsable(t.Context())
		if err != nil {
			t.Fatalf("FirstUsable failed: %v", err)
		}
		if backend.String() != "proot" {
			t.Errorf("expected backend 'proot', got %q", backend.String())
		}
	})

	t.Run("AppleContainerUsableWhenDockerAbsent", func(t *testing.T) {
		oldLookPath := lookPathFunction
		defer func() { lookPathFunction = oldLookPath }()
		lookPathFunction = func(file string) (string, error) {
			if file == "container" {
				return "/bin/container", nil
			}
			return "", exec.ErrNotFound
		}

		oldAppleOperatingSystem := appleContainerOperatingSystem
		defer func() { appleContainerOperatingSystem = oldAppleOperatingSystem }()
		appleContainerOperatingSystem = "darwin"

		backend, err := FirstUsable(t.Context())
		if err != nil {
			t.Fatalf("FirstUsable failed: %v", err)
		}
		if backend.String() != "apple-container" {
			t.Errorf("expected backend 'apple-container', got %q", backend.String())
		}
	})
}

func TestFirstUsableNothingUsableErrorListsAll(t *testing.T) {
	oldLookPath := lookPathFunction
	defer func() { lookPathFunction = oldLookPath }()
	lookPathFunction = func(file string) (string, error) {
		return "", exec.ErrNotFound
	}

	oldAppleOperatingSystem := appleContainerOperatingSystem
	defer func() { appleContainerOperatingSystem = oldAppleOperatingSystem }()
	appleContainerOperatingSystem = "linux"

	oldCheckUserNamespaces := checkUserNamespacesFunction
	defer func() { checkUserNamespacesFunction = oldCheckUserNamespaces }()
	checkUserNamespacesFunction = func(ctx context.Context) error {
		return fmt.Errorf("unprivileged user namespaces are not available for chroot: enable kernel user namespaces")
	}

	oldProotOperatingSystem := prootOperatingSystem
	defer func() { prootOperatingSystem = oldProotOperatingSystem }()
	prootOperatingSystem = "linux"

	backend, err := FirstUsable(t.Context())
	if err == nil {
		t.Fatalf("expected error when nothing usable, got backend %v", backend)
	}

	errorMessage := err.Error()
	expectedBackends := []string{"docker", "apple-container", "chroot", "proot"}
	for _, expectedBackend := range expectedBackends {
		if !strings.Contains(errorMessage, "- "+expectedBackend+":") {
			t.Errorf("expected error to list backend %q, got:\n%s", expectedBackend, errorMessage)
		}
	}

	expectedReasons := []string{
		"docker executable not found",
		"apple-container requires macOS",
		"unprivileged user namespaces are not available for chroot",
		"proot executable not found",
	}
	for _, expectedReason := range expectedReasons {
		if !strings.Contains(errorMessage, expectedReason) {
			t.Errorf("expected error to contain reason %q, got:\n%s", expectedReason, errorMessage)
		}
	}
}

func TestFirstUsableHonoursWithExecutable(t *testing.T) {
	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand

	t.Run("CustomDockerExecutable", func(t *testing.T) {
		customDockerPath := "/custom/bin/custom-docker"
		oldLookPath := lookPathFunction
		defer func() { lookPathFunction = oldLookPath }()
		lookPathFunction = func(file string) (string, error) {
			if file == customDockerPath {
				return customDockerPath, nil
			}
			return "", exec.ErrNotFound
		}

		backend, err := FirstUsable(t.Context(), WithExecutable("docker", customDockerPath))
		if err != nil {
			t.Fatalf("FirstUsable failed: %v", err)
		}
		if backend.String() != "docker" {
			t.Errorf("expected backend 'docker', got %q", backend.String())
		}
	})

	t.Run("CustomProotExecutable", func(t *testing.T) {
		customProotPath := "/custom/bin/custom-proot"
		oldLookPath := lookPathFunction
		defer func() { lookPathFunction = oldLookPath }()
		lookPathFunction = func(file string) (string, error) {
			if file == customProotPath {
				return customProotPath, nil
			}
			return "", exec.ErrNotFound
		}

		oldAppleOperatingSystem := appleContainerOperatingSystem
		defer func() { appleContainerOperatingSystem = oldAppleOperatingSystem }()
		appleContainerOperatingSystem = "linux"

		oldCheckUserNamespaces := checkUserNamespacesFunction
		defer func() { checkUserNamespacesFunction = oldCheckUserNamespaces }()
		checkUserNamespacesFunction = func(ctx context.Context) error {
			return fmt.Errorf("user namespaces disabled")
		}

		oldProotOperatingSystem := prootOperatingSystem
		defer func() { prootOperatingSystem = oldProotOperatingSystem }()
		prootOperatingSystem = "linux"

		backend, err := FirstUsable(t.Context(), WithExecutable("proot", customProotPath))
		if err != nil {
			t.Fatalf("FirstUsable failed: %v", err)
		}
		if backend.String() != "proot" {
			t.Errorf("expected backend 'proot', got %q", backend.String())
		}
	})
}

func TestDockerUsableEngineNotResponding(t *testing.T) {
	oldLookPath := lookPathFunction
	defer func() { lookPathFunction = oldLookPath }()
	lookPathFunction = func(file string) (string, error) {
		if file == "docker" {
			return "/bin/docker", nil
		}
		return "", exec.ErrNotFound
	}

	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand

	t.Setenv("MOCK_BEHAVIOR", "docker_info_error")

	backend, err := New("docker")
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	err = backend.Usable(t.Context())
	if err == nil {
		t.Fatal("expected error when docker engine not responding, got nil")
	}

	expectedMessage := "docker engine is not responding: start the Docker service or verify permissions"
	if !strings.Contains(err.Error(), expectedMessage) {
		t.Errorf("expected error %q, got %q", expectedMessage, err.Error())
	}
}

func TestAppleContainerUsableServiceNotRunning(t *testing.T) {
	oldLookPath := lookPathFunction
	defer func() { lookPathFunction = oldLookPath }()
	lookPathFunction = func(file string) (string, error) {
		if file == "container" {
			return "/bin/container", nil
		}
		return "", exec.ErrNotFound
	}

	oldAppleOperatingSystem := appleContainerOperatingSystem
	defer func() { appleContainerOperatingSystem = oldAppleOperatingSystem }()
	appleContainerOperatingSystem = "darwin"

	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand

	t.Setenv("MOCK_BEHAVIOR", "container_service_error")

	backend, err := New("apple-container")
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	err = backend.Usable(t.Context())
	if err == nil {
		t.Fatal("expected error when apple-container service is not running, got nil")
	}

	expectedMessage := "apple-container service is not running: start the service with 'container system start'"
	if !strings.Contains(err.Error(), expectedMessage) {
		t.Errorf("expected error %q, got %q", expectedMessage, err.Error())
	}
}

func TestUsableContextCancellation(t *testing.T) {
	cancelledContext, cancelFunction := context.WithCancel(t.Context())
	cancelFunction()

	backend, err := New("docker")
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	err = backend.Usable(cancelledContext)
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected errors.Is(err, context.Canceled), got %v", err)
	}

	_, err = FirstUsable(cancelledContext)
	if err == nil {
		t.Fatal("expected error from FirstUsable on cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected errors.Is(err, context.Canceled), got %v", err)
	}
}

func TestFirstUsableCancelledDuringProbe(t *testing.T) {
	oldLookPath := lookPathFunction
	defer func() { lookPathFunction = oldLookPath }()
	lookPathFunction = func(file string) (string, error) {
		if file == "docker" {
			return "/bin/docker", nil
		}
		return "", exec.ErrNotFound
	}

	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand

	t.Setenv("MOCK_BEHAVIOR", "sleep")

	probeContext, cancelProbe := context.WithCancel(t.Context())

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancelProbe()
	}()

	backend, err := FirstUsable(probeContext)
	if err == nil {
		t.Fatalf("expected error from FirstUsable on cancelled context, got backend %v", backend)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected errors.Is(err, context.Canceled), got %v", err)
	}
	if strings.Contains(err.Error(), "no usable sandbox backend found") {
		t.Errorf("expected prompt cancellation error, got swallowed failure reasons:\n%s", err.Error())
	}
}

func TestCheckUsable(t *testing.T) {
	oldLookPath := lookPathFunction
	defer func() { lookPathFunction = oldLookPath }()
	lookPathFunction = func(file string) (string, error) {
		if file == "docker" {
			return "/bin/docker", nil
		}
		return "", exec.ErrNotFound
	}

	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand

	if err := CheckUsable(t.Context(), "docker"); err != nil {
		t.Errorf("expected docker to be usable, got %v", err)
	}

	if err := CheckUsable(t.Context(), "unknown-backend"); err == nil {
		t.Errorf("expected error for unknown backend, got nil")
	}
}
