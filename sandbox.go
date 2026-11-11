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

package main

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/crane"
)

// Sandbox is the interface implemented by all our sandboxing technologies (docker, chroot etc)
type Sandbox interface {
	// Run executes the container image defined by script
	Run(stdin io.Reader, stdout, stderr io.Writer, script Script, args []string) error
}

func prepareRootFS(imageRef string) (string, string, func(), error) {
	// Assume it is a container image
	img, err := crane.Pull(imageRef)
	if err != nil {
		return "", "", nil, fmt.Errorf("pulling image %q: %w", imageRef, err)
	}

	digest, err := img.Digest()
	if err != nil {
		return "", "", nil, fmt.Errorf("getting image digest: %w", err)
	}
	imageSHA := digest.Hex

	tmpDir, err := os.MkdirTemp("", "clix-chroot-*")
	if err != nil {
		return "", "", nil, err
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	// Export to tar stream
	pr, pw := io.Pipe()
	go func() {
		err := crane.Export(img, pw)
		pw.CloseWithError(err)
	}()

	if err := untar(pr, tmpDir); err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("unpacking image: %w", err)
	}

	return tmpDir, imageSHA, cleanup, nil
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
	if copyErr != nil {
		return copyErr
	}
	if chmodErr != nil {
		return chmodErr
	}
	return closeErr
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

func resolveMounts(mounts []Mount, imageSHA string) ([]Mount, error) {
	var resolved []Mount
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user home dir: %w", err)
	}

	for _, m := range mounts {
		if strings.Contains(m.HostPath, "{cacheDir}") || strings.Contains(m.HostPath, "${cacheDir}") {
			if strings.Count(m.HostPath, "{cacheDir}") > strings.Count(m.HostPath, "${cacheDir}") {
				fmt.Fprintf(os.Stderr, "Warning: usage of {cacheDir} is deprecated and will be removed in future versions. Please use ${cacheDir} instead.\n")
			}
			if imageSHA == "" {
				return nil, fmt.Errorf("cacheDir variable used but image SHA not available")
			}
			userCache, err := os.UserCacheDir()
			if err != nil {
				return nil, fmt.Errorf("failed to get user cache dir: %w", err)
			}
			// TODO: Eventually we'll need to do garbage collection
			cacheDir := filepath.Join(userCache, "clix", "cache", imageSHA)
			if err := os.MkdirAll(cacheDir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create cache dir: %w", err)
			}
			m.HostPath = strings.ReplaceAll(m.HostPath, "${cacheDir}", cacheDir)
			m.HostPath = strings.ReplaceAll(m.HostPath, "{cacheDir}", cacheDir)
		}

		if m.HostPath == "git.repoRoot(cwd)" {
			root, err := findGitRoot(cwd)
			if err != nil {
				return nil, fmt.Errorf("failed to find git root: %w", err)
			}
			m.HostPath = root
		}

		if strings.HasPrefix(m.HostPath, "~/") {
			m.HostPath = filepath.Join(home, m.HostPath[2:])
		} else if m.HostPath == "~" {
			m.HostPath = home
		}

		// TODO: Resolve this better once we find a container image where HOME is not /root
		if strings.HasPrefix(m.SandboxPath, "~/") {
			m.SandboxPath = "/root/" + m.SandboxPath[2:]
		} else if m.SandboxPath == "~" {
			m.SandboxPath = "/root"
		}

		if m.SandboxPath == "" {
			m.SandboxPath = m.HostPath
		}
		resolved = append(resolved, m)
	}
	return resolved, nil
}

func findGitRoot(path string) (string, error) {
	cmd := execCommand("git", "rev-parse", "--show-toplevel")
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
