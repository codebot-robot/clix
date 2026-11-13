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
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// Sandbox is the interface implemented by all our sandboxing technologies (docker, chroot etc)
type Sandbox interface {
	// Run executes the container image defined by script
	Run(stdin io.Reader, stdout, stderr io.Writer, script Script, args []string) error
}

type preparedRootFS struct {
	dir      string
	imageSHA string
	env      []string
	cleanup  func()
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

	digest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("getting image digest: %w", err)
	}
	imageSHA := digest.Hex

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
		dir:      tmpDir,
		imageSHA: imageSHA,
		env:      env,
		cleanup:  cleanup,
	}, nil
}

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

func untar(r io.Reader, dest string) error {
	// Canonicalise dest so symlink-resolution checks below compare against the
	// real path (e.g. /tmp -> /private/tmp on some platforms).
	dest, err := filepath.EvalSymlinks(dest)
	if err != nil {
		return err
	}

	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		path := filepath.Join(dest, header.Name)

		// Lexical zip-slip protection on the entry name itself.
		if !inDest(dest, path) {
			return fmt.Errorf("illegal file path in image: %s", header.Name)
		}

		// Symlink-following protection: a previously extracted symlink must not
		// redirect this entry's parent directory outside dest. This guards
		// MkdirAll / Create / Chmod / Symlink below regardless of Typeflag.
		if err := checkPathInDest(dest, filepath.Dir(path)); err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			// Ensure parent dir exists
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
			os.Chmod(path, os.FileMode(header.Mode))
		case tar.TypeSymlink:
			// Reject link targets that escape dest. Absolute targets are always
			// rejected; relative targets are resolved lexically against the
			// link's directory and must stay within dest. This is belt-and-
			// braces alongside checkPathInDest above (which catches traversal
			// through an already-planted link).
			target := header.Linkname
			if filepath.IsAbs(target) || !inDest(dest, filepath.Join(filepath.Dir(path), target)) {
				return fmt.Errorf("illegal symlink target in image: %s -> %s", header.Name, target)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			if err := os.Symlink(target, path); err != nil {
				return err
			}
		}
	}
	return nil
}

// inDest reports whether p is dest or a path strictly beneath it.
func inDest(dest, p string) bool {
	return p == dest || strings.HasPrefix(p, dest+string(os.PathSeparator))
}

// checkPathInDest resolves all symlinks in p (or, if p does not yet exist, its
// nearest existing ancestor) and verifies the result is still within dest.
func checkPathInDest(dest, p string) error {
	for {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				parent := filepath.Dir(p)
				if parent == p {
					return err
				}
				p = parent
				continue
			}
			return err
		}
		if !inDest(dest, resolved) {
			return fmt.Errorf("illegal file path in image: %s resolves outside rootfs", p)
		}
		return nil
	}
}

func resolveMounts(mounts []Mount, imageSHA, scriptKey string) ([]Mount, error) {
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
		origHostPath := m.HostPath
		var cacheDir string
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
			cacheDir = filepath.Join(userCache, "clix", "cache", imageSHA, scriptKey)
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

		m.HostPath = filepath.Clean(m.HostPath)

		if m.SandboxPath == "" {
			m.SandboxPath = m.HostPath
		}

		if err := validateMount(m, origHostPath, cacheDir); err != nil {
			return nil, err
		}

		resolved = append(resolved, m)
	}
	return resolved, nil
}

func validateMount(m Mount, origHostPath, cacheDir string) error {
	if !filepath.IsAbs(m.HostPath) {
		return fmt.Errorf("mount %q: host path is not absolute", origHostPath)
	}
	if cacheDir != "" {
		rel, err := filepath.Rel(cacheDir, m.HostPath)
		if err != nil || !filepath.IsLocal(rel) {
			return fmt.Errorf("mount %q: host path escapes cache directory", origHostPath)
		}
	}
	if !filepath.IsAbs(m.SandboxPath) {
		return fmt.Errorf("mount %q: sandbox path %q is not absolute", origHostPath, m.SandboxPath)
	}
	if strings.Contains(m.HostPath, ":") {
		return fmt.Errorf("mount %q: host path contains ':'", origHostPath)
	}
	if strings.Contains(m.SandboxPath, ":") {
		return fmt.Errorf("mount %q: sandbox path %q contains ':'", origHostPath, m.SandboxPath)
	}
	return nil
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
