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

// Package sandbox provides execution of container images in isolated sandboxes
// across multiple backends (Docker, Apple Container, Chroot, and Proot).
package sandbox

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// ExecCommand is the function used to create exec.Cmd instances.
// It defaults to exec.Command and can be overridden by callers or tests.
var ExecCommand = exec.Command

func log(level int, format string, v ...any) {
	verbosity := 0
	if verbosityString := os.Getenv("CLIX_LOG_VERBOSITY"); verbosityString != "" {
		if _, err := fmt.Sscanf(verbosityString, "%d", &verbosity); err != nil {
			fmt.Fprintf(os.Stderr, "clix: failed to parse CLIX_LOG_VERBOSITY %q: %v\n", verbosityString, err)
		}
	}
	if verbosity >= level {
		fmt.Fprintf(os.Stderr, "clix: "+format+"\n", v...)
	}
}

// Sandbox is the interface implemented by all sandboxing backends.
type Sandbox interface {
	// Run executes the container image described by config with the provided command arguments.
	Run(stdin io.Reader, stdout, stderr io.Writer, config Config, args []string) error
}

// Config describes the execution of a container image in a sandbox.
type Config struct {
	// Image is the container image reference to run.
	Image string

	// Entrypoint overrides the image entrypoint if non-empty.
	Entrypoint string

	// Mounts lists the filesystem mounts to expose inside the sandbox.
	Mounts []Mount

	// Env lists the environment variables visible to the sandboxed process.
	Env []EnvVar

	// WorkingDir specifies the working directory inside the sandbox.
	// If empty, backends default to the host current working directory (for Docker and Apple Container)
	// or "/" (for Chroot and Proot).
	WorkingDir string
}

// Mount describes a host directory mounted into the sandbox.
type Mount struct {
	// HostPath is the absolute path on the host to mount.
	HostPath string `json:"hostPath"`

	// SandboxPath is the absolute path inside the sandbox where HostPath is mounted.
	// If empty, it defaults to HostPath.
	SandboxPath string `json:"sandboxPath,omitempty"`

	// ReadOnly specifies whether the mount is read-only.
	ReadOnly bool `json:"readOnly,omitempty"`
}

// EnvVar defines an environment variable to pass to the sandboxed process.
type EnvVar struct {
	// Name is the environment variable name.
	Name string `json:"name"`

	// Value is the literal environment variable value.
	Value string `json:"value"`
}

// New returns a Sandbox implementation for the specified backend name.
// Supported backends are "docker" (the default if backend is empty), "apple-container",
// "chroot", and "proot". If an unknown backend is requested, New returns an error.
func New(backend string) (Sandbox, error) {
	switch backend {
	case "", "docker":
		return &DockerSandbox{}, nil
	case "apple-container":
		return &AppleContainerSandbox{}, nil
	case "chroot":
		return &ChrootSandbox{}, nil
	case "proot":
		return &ProotSandbox{}, nil
	default:
		return nil, fmt.Errorf("unknown sandbox backend %q", backend)
	}
}

// NewFromEnv returns a Sandbox based on the CLIX_SANDBOX environment variable.
// If CLIX_SANDBOX is unset or empty, the default Docker backend is used.
func NewFromEnv() (Sandbox, error) {
	return New(os.Getenv("CLIX_SANDBOX"))
}

// validateMount validates and normalizes a mount.
func validateMount(m Mount) (Mount, error) {
	if !filepath.IsAbs(m.HostPath) {
		return m, fmt.Errorf("mount %q: host path is not absolute", m.HostPath)
	}
	if m.SandboxPath == "" {
		m.SandboxPath = m.HostPath
	}
	if !filepath.IsAbs(m.SandboxPath) {
		return m, fmt.Errorf("mount %q: sandbox path %q is not absolute", m.HostPath, m.SandboxPath)
	}
	if strings.Contains(m.HostPath, ":") {
		return m, fmt.Errorf("mount %q: host path contains ':'", m.HostPath)
	}
	if strings.Contains(m.SandboxPath, ":") {
		return m, fmt.Errorf("mount %q: sandbox path %q contains ':'", m.HostPath, m.SandboxPath)
	}
	return m, nil
}

// BuildEnv constructs the environment variable list for the sandbox per the sandbox contract.
// It includes minimal base variables (PATH, HOME, and TERM if set on host), then variables
// from the image config, and finally explicitly declared configEnv variables (which override earlier ones).
// No other host variables are inherited.
func BuildEnv(imageEnv []string, configEnv []EnvVar) []string {
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

	for _, env := range configEnv {
		set(env.Name, env.Value)
	}

	result := make([]string, 0, len(order))
	for _, name := range order {
		result = append(result, name+"="+envMap[name])
	}
	return result
}

type preparedRootFS struct {
	dir      string
	imageSHA string
	env      []string
	cleanup  func()
}

func imageEnv(img v1.Image) ([]string, error) {
	configFile, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("reading image config: %w", err)
	}
	if configFile == nil {
		return nil, nil
	}
	return configFile.Config.Env, nil
}

func prepareRootFS(imageRef string) (*preparedRootFS, error) {
	img, err := crane.Pull(imageRef)
	if err != nil {
		return nil, fmt.Errorf("pulling image %q: %w", imageRef, err)
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("getting image digest: %w", err)
	}
	imageSHA := digest.Hex

	env, err := imageEnv(img)
	if err != nil {
		return nil, err
	}

	tempDir, err := os.MkdirTemp("", "clix-chroot-*")
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		if err := os.RemoveAll(tempDir); err != nil {
			fmt.Fprintf(os.Stderr, "failed to remove temp dir %s: %v\n", tempDir, err)
		}
	}

	// Export to tar stream
	pipeReader, pipeWriter := io.Pipe()
	go func() {
		err := crane.Export(img, pipeWriter)
		pipeWriter.CloseWithError(err)
	}()

	if err := untar(pipeReader, tempDir); err != nil {
		cleanup()
		return nil, fmt.Errorf("unpacking image: %w", err)
	}

	return &preparedRootFS{
		dir:      tempDir,
		imageSHA: imageSHA,
		env:      env,
		cleanup:  cleanup,
	}, nil
}

func untar(r io.Reader, destination string) (retErr error) {
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()

	tarReader := tar.NewReader(r)
	for {
		header, err := tarReader.Next()
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
			if err := extractReg(root, name, header, tarReader); err != nil {
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
	fileInfo, err := root.Lstat(name)
	if err == nil {
		if !fileInfo.IsDir() {
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
