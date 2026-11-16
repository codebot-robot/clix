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

var execCommand = exec.Command

func log(level int, format string, v ...any) {
	verbosity := 0
	if vStr := os.Getenv("CLIX_LOG_VERBOSITY"); vStr != "" {
		if _, err := fmt.Sscanf(vStr, "%d", &verbosity); err != nil {
			fmt.Fprintf(os.Stderr, "clix: failed to parse CLIX_LOG_VERBOSITY %q: %v\n", vStr, err)
		}
	}
	if verbosity >= level {
		fmt.Fprintf(os.Stderr, "clix: "+format+"\n", v...)
	}
}

// Sandbox is the interface implemented by all our sandboxing technologies (docker, chroot etc)
type Sandbox interface {
	// Run executes the container image defined by script
	Run(stdin io.Reader, stdout, stderr io.Writer, script Config, args []string) error
}

type Config struct {
	Image      string   `json:"image,omitempty"`
	Entrypoint string   `json:"entrypoint,omitempty"`
	Mounts     []Mount  `json:"mounts,omitempty"`
	Env        []EnvVar `json:"env,omitempty"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Mount struct {
	HostPath         string `json:"hostPath"`
	SandboxPath      string `json:"sandboxPath,omitempty"`
	ReadOnly         bool   `json:"readOnly,omitempty"`
	WritableFallback bool   `json:"-"`
}

// ExitError indicates that the executed tool exited with a non-zero exit code.
type ExitError struct {
	exitCode int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.exitCode)
}

func (e *ExitError) ExitCode() int {
	return e.exitCode
}

func NewExitError(exitCode int) *ExitError {
	return &ExitError{exitCode: exitCode}
}

func newExitError(exitCode int) *ExitError {
	return NewExitError(exitCode)
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

func prepareRootFS(imageRef string) (*preparedRootFS, error) {
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
			fmt.Fprintf(os.Stderr, "failed to remove temp dir %s: %v\n", tmpDir, err)
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
