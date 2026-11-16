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
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

var execCommand = exec.Command

// Sandbox is the execution environment for running a container image.
type Sandbox interface {
	// Run executes the container image described by config with args.
	// Stdin, stdout, and stderr are wired to the process.
	// If the executed tool exits with a non-zero status, Run returns an
	// error wrapping *ExitError.
	Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, config Config, args []string) error
}

// Config describes a container run within a sandbox backend.
type Config struct {
	// Image specifies the container image name, reference, or path.
	Image string `json:"image,omitempty"`
	// Entrypoint overrides the container image entrypoint if non-empty.
	Entrypoint string `json:"entrypoint,omitempty"`
	// Mounts lists host directories mounted into the sandbox container.
	Mounts []Mount `json:"mounts,omitempty"`
	// Env lists environment variables explicitly passed into the sandbox.
	Env []EnvVar `json:"env,omitempty"`
	// Network specifies network isolation for the sandbox.
	// An empty string keeps default network access.
	// "none" disables network access.
	Network string `json:"network,omitempty"`
}

// EnvVar describes an environment variable passed into the sandbox.
type EnvVar struct {
	// Name is the environment variable name.
	Name string `json:"name"`
	// Value is the literal environment variable value.
	Value string `json:"value"`
}

// Mount describes a host directory mounted into the sandbox.
type Mount struct {
	// HostPath is the path to the directory on the host.
	HostPath string `json:"hostPath"`
	// SandboxPath is an absolute path inside the sandbox, required.
	SandboxPath string `json:"sandboxPath,omitempty"`
	// ReadOnly specifies whether the mount should be read-only.
	ReadOnly bool `json:"readOnly,omitempty"`
	// WritableFallback allows backends that do not support read-only mounts
	// (such as proot) to fall back to a writable mount.
	WritableFallback bool `json:"-"`
}

// ExitError indicates that the executed tool exited with a non-zero exit code.
// Callers can inspect the code using errors.As(err, &exitError).
type ExitError struct {
	exitCode int
}

// Error returns a formatted error string including the exit code.
func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.exitCode)
}

// ExitCode returns the tool's non-zero exit code.
func (e *ExitError) ExitCode() int {
	return e.exitCode
}

// NewExitError constructs an *ExitError with the given exitCode.
func NewExitError(exitCode int) *ExitError {
	return &ExitError{exitCode: exitCode}
}

func newExitError(exitCode int) *ExitError {
	return NewExitError(exitCode)
}

// New returns a Sandbox implementation for the given backend name.
// If backendName is empty, it defaults to "docker".
// Valid backend names are "docker", "apple-container", "chroot", and "proot".
// If the backend name is unrecognised, New returns an error.
func New(backendName string) (Sandbox, error) {
	switch backendName {
	case "", "docker":
		return &dockerSandbox{}, nil
	case "apple-container":
		return &appleContainerSandbox{}, nil
	case "chroot":
		return &chrootSandbox{}, nil
	case "proot":
		return &prootSandbox{}, nil
	default:
		return nil, fmt.Errorf("unknown sandbox backend %q", backendName)
	}
}

// FromEnvironment returns a Sandbox chosen by the CLIX_SANDBOX environment
// variable, defaulting to "docker" if unset.
func FromEnvironment() (Sandbox, error) {
	return New(os.Getenv("CLIX_SANDBOX"))
}

type preparedRootFS struct {
	dir     string
	env     []string
	cleanup func()
}

func imageEnv(img v1.Image) ([]string, error) {
	cf, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("reading image config: %w", err)
	}
	if cf == nil {
		return nil, nil
	}
	return cf.Config.Env, nil
}

func prepareRootFS(ctx context.Context, imageRef string) (*preparedRootFS, error) {
	// Assume it is a container image
	img, err := crane.Pull(imageRef)
	if err != nil {
		return nil, fmt.Errorf("pulling image %q: %w", imageRef, err)
	}

	env, err := imageEnv(img)
	if err != nil {
		return nil, err
	}

	tmpDir, err := os.MkdirTemp("", "clix-chroot-*")
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			logger := logr.FromContextOrDiscard(ctx)
			logger.Error(err, "failed to remove temp dir", "directory", tmpDir)
		}
	}

	// Export to tar stream
	pr, pw := io.Pipe()
	go func() {
		err := crane.Export(img, pw)
		pw.CloseWithError(err)
	}()

	if err := untar(pr, tmpDir); err != nil {
		cleanup()
		return nil, fmt.Errorf("unpacking image: %w", err)
	}

	return &preparedRootFS{
		dir:     tmpDir,
		env:     env,
		cleanup: cleanup,
	}, nil
}

var prepareRootFSFn = prepareRootFS

func buildSandboxEnv(imageEnv []string, scriptEnv []EnvVar) []string {
	var order []string
	envMap := make(map[string]string)
	set := func(name, value string) {
		if name == "" {
			return
		}
		if _, exists := envMap[name]; !exists {
			order = append(order, name)
		}
		envMap[name] = value
	}

	set("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	set("HOME", "/root")
	if term := os.Getenv("TERM"); term != "" {
		set("TERM", term)
	}

	for _, entry := range imageEnv {
		name, value, _ := strings.Cut(entry, "=")
		set(name, value)
	}

	for _, env := range scriptEnv {
		set(env.Name, env.Value)
	}

	result := make([]string, 0, len(order))
	for _, name := range order {
		result = append(result, name+"="+envMap[name])
	}
	return result
}

func untar(r io.Reader, dest string) (retErr error) {
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()

	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		name := strings.TrimPrefix(header.Name, "./")
		name = strings.TrimPrefix(name, "/")
		if name == "" || name == "." {
			continue
		}
		if !filepath.IsLocal(name) {
			return fmt.Errorf("illegal entry name %q", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := extractDir(root, name); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := extractReg(root, name, header, tr); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := extractSymlink(root, name, header); err != nil {
				return err
			}
		}
	}
	return nil
}

func removeNonDir(root *os.Root, name string) error {
	fi, err := root.Lstat(name)
	if err == nil {
		if !fi.IsDir() {
			return root.Remove(name)
		}
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func extractDir(root *os.Root, name string) error {
	return root.MkdirAll(name, 0755)
}

func extractReg(root *os.Root, name string, header *tar.Header, r io.Reader) error {
	if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return err
	}
	if err := removeNonDir(root, name); err != nil {
		return err
	}
	perm := header.FileInfo().Mode().Perm()
	f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, r)
	chmodErr := f.Chmod(perm)
	closeErr := f.Close()
	return errors.Join(copyErr, chmodErr, closeErr)
}

func extractSymlink(root *os.Root, name string, header *tar.Header) error {
	if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return err
	}
	if err := removeNonDir(root, name); err != nil {
		return err
	}
	return root.Symlink(header.Linkname, name)
}
