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
	"io"
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

func TestDockerUsable(t *testing.T) {
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

	backend, err := New("docker")
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	t.Run("EngineNotResponding", func(t *testing.T) {
		t.Setenv("MOCK_BEHAVIOR", "docker_info_error")

		err := backend.Usable(t.Context())
		if err == nil {
			t.Fatal("expected error when docker engine not responding, got nil")
		}

		expectedMessage := "docker engine is not responding: start the Docker service or verify permissions"
		if !strings.Contains(err.Error(), expectedMessage) || !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
			t.Errorf("expected actionable reason and tool output, got %q", err.Error())
		}
		var exitError *ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
			t.Errorf("expected underlying ExitError with exit code 1, got %v", err)
		}
	})

	t.Run("UnrelatedFailure", func(t *testing.T) {
		t.Setenv("MOCK_BEHAVIOR", "docker_info_permission_denied")

		err := backend.Usable(t.Context())
		if err == nil {
			t.Fatal("expected error when docker fails with permission denied, got nil")
		}

		expectedMessage := "docker engine is not responding: start the Docker service or verify permissions"
		if !strings.Contains(err.Error(), expectedMessage) || !strings.Contains(err.Error(), "permission denied while trying to connect") {
			t.Errorf("expected actionable reason and tool output, got %q", err.Error())
		}
		if strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
			t.Errorf("expected unrelated failure to report its own message, got %q", err.Error())
		}
		var exitError *ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
			t.Errorf("expected underlying ExitError with exit code 1, got %v", err)
		}
	})
}

func TestAppleContainerUsable(t *testing.T) {
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

	backend, err := New("apple-container")
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	t.Run("ServiceNotRunning", func(t *testing.T) {
		t.Setenv("MOCK_BEHAVIOR", "container_service_error")

		err := backend.Usable(t.Context())
		if err == nil {
			t.Fatal("expected error when apple-container service is not running, got nil")
		}

		expectedMessage := "apple-container service is not running: start the service with 'container system start'"
		if !strings.Contains(err.Error(), expectedMessage) || !strings.Contains(err.Error(), "apiserver is not running and not registered with launchd") {
			t.Errorf("expected actionable reason and tool output, got %q", err.Error())
		}
		var exitError *ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
			t.Errorf("expected underlying ExitError with exit code 1, got %v", err)
		}
	})

	t.Run("UnrelatedFailure", func(t *testing.T) {
		t.Setenv("MOCK_BEHAVIOR", "container_service_unrelated_error")

		err := backend.Usable(t.Context())
		if err == nil {
			t.Fatal("expected error when apple-container fails with unrelated error, got nil")
		}

		expectedMessage := "apple-container service is not running: start the service with 'container system start'"
		if !strings.Contains(err.Error(), expectedMessage) || !strings.Contains(err.Error(), "unrecognized argument: invalid flag --unknown") {
			t.Errorf("expected actionable reason and tool output, got %q", err.Error())
		}
		if strings.Contains(err.Error(), "apiserver is not running and not registered with launchd") {
			t.Errorf("expected unrelated failure to report its own message, got %q", err.Error())
		}
		var exitError *ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
			t.Errorf("expected underlying ExitError with exit code 1, got %v", err)
		}
	})
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

func TestFirstUsableRequirements(t *testing.T) {
	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand

	t.Run("RequireMountsSkipsChrootAndSelectsProot", func(t *testing.T) {
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
			return nil
		}

		oldProotOperatingSystem := prootOperatingSystem
		defer func() { prootOperatingSystem = oldProotOperatingSystem }()
		prootOperatingSystem = "linux"

		// Without requirements, chroot is returned.
		backend, err := FirstUsable(t.Context())
		if err != nil {
			t.Fatalf("FirstUsable failed: %v", err)
		}
		if backend.String() != "chroot" {
			t.Errorf("expected backend 'chroot' without requirements, got %q", backend.String())
		}

		// With WithRequirements(RequireMounts), chroot is skipped because it lacks mounts; proot is returned.
		backend, err = FirstUsable(t.Context(), WithRequirements(RequireMounts))
		if err != nil {
			t.Fatalf("FirstUsable(WithRequirements(RequireMounts)) failed: %v", err)
		}
		if backend.String() != "proot" {
			t.Errorf("expected backend 'proot' with WithRequirements(RequireMounts), got %q", backend.String())
		}
	})

	t.Run("RequireNetworkNoneSelectsAppleContainer", func(t *testing.T) {
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

		oldCheckUserNamespaces := checkUserNamespacesFunction
		defer func() { checkUserNamespacesFunction = oldCheckUserNamespaces }()
		checkUserNamespacesFunction = func(ctx context.Context) error {
			return fmt.Errorf("chroot not usable")
		}

		oldProotOperatingSystem := prootOperatingSystem
		defer func() { prootOperatingSystem = oldProotOperatingSystem }()
		prootOperatingSystem = "darwin"

		// Without requirements, apple-container is returned on darwin.
		backend, err := FirstUsable(t.Context())
		if err != nil {
			t.Fatalf("FirstUsable failed: %v", err)
		}
		if backend.String() != "apple-container" {
			t.Errorf("expected backend 'apple-container', got %q", backend.String())
		}

		// With RequireNetworkNone, apple-container is selected because it supports network isolation.
		backend, err = FirstUsable(t.Context(), WithRequirements(RequireNetworkNone))
		if err != nil {
			t.Fatalf("FirstUsable(WithRequirements(RequireNetworkNone)) failed: %v", err)
		}
		if backend.String() != "apple-container" {
			t.Errorf("expected backend 'apple-container', got %q", backend.String())
		}
	})

	t.Run("RequireNetworkNoneSkipsProot", func(t *testing.T) {
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
			return fmt.Errorf("chroot not usable")
		}

		oldProotOperatingSystem := prootOperatingSystem
		defer func() { prootOperatingSystem = oldProotOperatingSystem }()
		prootOperatingSystem = "linux"

		_, err := FirstUsable(t.Context(), WithRequirements(RequireNetworkNone))
		if err == nil {
			t.Fatal("expected error when no backend qualifies, got nil")
		}
		if !strings.Contains(err.Error(), "- proot: does not support no network") {
			t.Errorf("expected error to indicate proot does not support no network, got:\n%s", err.Error())
		}
	})

	t.Run("RequireReadOnlyMountsSkipsChrootAndProot", func(t *testing.T) {
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
			return nil
		}

		oldProotOperatingSystem := prootOperatingSystem
		defer func() { prootOperatingSystem = oldProotOperatingSystem }()
		prootOperatingSystem = "linux"

		_, err := FirstUsable(t.Context(), WithRequirements(RequireReadOnlyMounts))
		if err == nil {
			t.Fatal("expected error when no backend qualifies, got nil")
		}
		if !strings.Contains(err.Error(), "- chroot: does not support read-only mounts") && !strings.Contains(err.Error(), "- chroot: does not support mounts") {
			t.Errorf("expected chroot lacking mounts in error, got:\n%s", err.Error())
		}
		if !strings.Contains(err.Error(), "- proot: does not support read-only mounts") {
			t.Errorf("expected proot lacking read-only mounts in error, got:\n%s", err.Error())
		}
	})

	t.Run("NothingQualifiesErrorListsUnusableAndLacking", func(t *testing.T) {
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

		oldProotOperatingSystem := prootOperatingSystem
		defer func() { prootOperatingSystem = oldProotOperatingSystem }()
		prootOperatingSystem = "linux"

		_, err := FirstUsable(t.Context(), WithRequirements(RequireMounts))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		errorMessage := err.Error()
		if !strings.Contains(errorMessage, "- docker: docker executable not found") {
			t.Errorf("expected docker unusable reason, got:\n%s", errorMessage)
		}
		if !strings.Contains(errorMessage, "- apple-container: apple-container requires macOS") {
			t.Errorf("expected apple-container unusable reason, got:\n%s", errorMessage)
		}
		if !strings.Contains(errorMessage, "- chroot: does not support mounts") {
			t.Errorf("expected chroot lacking requirement reason, got:\n%s", errorMessage)
		}
		if !strings.Contains(errorMessage, "- proot: proot executable not found") {
			t.Errorf("expected proot unusable reason, got:\n%s", errorMessage)
		}
	})
}

func TestBackendCapabilitiesMatchRunRefusals(t *testing.T) {
	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand

	oldPrepare := prepareRootFSFunction
	defer func() { prepareRootFSFunction = oldPrepare }()
	prepareRootFSFunction = func(ctx context.Context, image string) (*preparedRootFS, error) {
		return &preparedRootFS{
			dir:     t.TempDir(),
			cleanup: func() {},
		}, nil
	}

	expectedCapabilities := map[string]Capabilities{
		"docker":          {Mounts: true, ReadOnlyMounts: true, NetworkNone: true},
		"apple-container": {Mounts: true, ReadOnlyMounts: true, NetworkNone: true},
		"chroot":          {Mounts: false, ReadOnlyMounts: false, NetworkNone: true},
		"proot":           {Mounts: true, ReadOnlyMounts: false, NetworkNone: false},
	}

	for _, backendName := range candidateBackends {
		t.Run(backendName, func(t *testing.T) {
			backend, err := New(backendName)
			if err != nil {
				t.Fatalf("New(%q) failed: %v", backendName, err)
			}
			capable, ok := backend.(Capable)
			if !ok {
				t.Fatalf("backend %q does not implement Capable", backendName)
			}
			capabilities := capable.Capabilities()
			if capabilities != expectedCapabilities[backendName] {
				t.Errorf("backend %q capabilities = %+v, expected %+v", backendName, capabilities, expectedCapabilities[backendName])
			}

			// Mounts
			mountConfig := Config{
				Image:  "alpine",
				Mounts: []Mount{{HostPath: "/tmp", SandboxPath: "/tmp"}},
			}
			err = backend.Run(t.Context(), mountConfig, []string{"echo"}, RunOptions{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard})
			refusesMounts := err != nil && strings.Contains(err.Error(), "mounts are not supported")
			if capabilities.Mounts && refusesMounts {
				t.Errorf("backend %s declared Mounts=true but Run refused mounts: %v", backendName, err)
			}
			if !capabilities.Mounts && !refusesMounts {
				t.Errorf("backend %s declared Mounts=false but Run did not refuse mounts: %v", backendName, err)
			}

			// Read-only Mounts
			readOnlyMountConfig := Config{
				Image:  "alpine",
				Mounts: []Mount{{HostPath: "/tmp", SandboxPath: "/tmp", ReadOnly: true}},
			}
			err = backend.Run(t.Context(), readOnlyMountConfig, []string{"echo"}, RunOptions{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard})
			refusesReadOnlyMounts := err != nil && (strings.Contains(err.Error(), "read-only mounts are not supported") || strings.Contains(err.Error(), "mounts are not supported"))
			if capabilities.ReadOnlyMounts && refusesReadOnlyMounts {
				t.Errorf("backend %s declared ReadOnlyMounts=true but Run refused read-only mounts: %v", backendName, err)
			}
			if !capabilities.ReadOnlyMounts && !refusesReadOnlyMounts {
				t.Errorf("backend %s declared ReadOnlyMounts=false but Run did not refuse read-only mounts: %v", backendName, err)
			}

			// Network None
			networkNoneConfig := Config{
				Image:   "alpine",
				Network: "none",
			}
			err = backend.Run(t.Context(), networkNoneConfig, []string{"echo"}, RunOptions{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard})
			refusesNetworkNone := err != nil && strings.Contains(err.Error(), "network isolation is not supported")
			if capabilities.NetworkNone && refusesNetworkNone {
				t.Errorf("backend %s declared NetworkNone=true but Run refused network none: %v", backendName, err)
			}
			if !capabilities.NetworkNone && !refusesNetworkNone {
				t.Errorf("backend %s declared NetworkNone=false but Run did not refuse network none: %v", backendName, err)
			}
		})
	}
}

func TestCheckUsableRequirements(t *testing.T) {
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

	oldCheckUserNamespaces := checkUserNamespacesFunction
	defer func() { checkUserNamespacesFunction = oldCheckUserNamespaces }()
	checkUserNamespacesFunction = func(ctx context.Context) error {
		return nil
	}

	// docker supports mounts
	if err := CheckUsable(t.Context(), "docker", WithRequirements(RequireMounts)); err != nil {
		t.Errorf("expected docker to satisfy RequireMounts, got %v", err)
	}

	// chroot does not support mounts
	if err := CheckUsable(t.Context(), "chroot", WithRequirements(RequireMounts)); err == nil {
		t.Error("expected error for chroot with WithRequirements(RequireMounts), got nil")
	} else if !strings.Contains(err.Error(), "chroot does not support mounts") {
		t.Errorf("expected error mentioning 'chroot does not support mounts', got %v", err)
	}
}
