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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gke-labs/clix/sandbox"
	"github.com/google/go-containerregistry/pkg/crane"
)

func resolveMounts(mounts []sandbox.Mount, imageSHA, scriptKey string) ([]sandbox.Mount, error) {
	var resolved []sandbox.Mount
	workingDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user home dir: %w", err)
	}

	for _, mount := range mounts {
		originalHostPath := mount.HostPath
		var cacheDir string
		if strings.Contains(mount.HostPath, "{cacheDir}") || strings.Contains(mount.HostPath, "${cacheDir}") {
			if strings.Count(mount.HostPath, "{cacheDir}") > strings.Count(mount.HostPath, "${cacheDir}") {
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
			mount.HostPath = strings.ReplaceAll(mount.HostPath, "${cacheDir}", cacheDir)
			mount.HostPath = strings.ReplaceAll(mount.HostPath, "{cacheDir}", cacheDir)
		}

		isRepoRoot := mount.HostPath == "git.repoRoot(cwd)"
		if isRepoRoot {
			root, err := findGitRoot(workingDir)
			if err != nil {
				return nil, fmt.Errorf("failed to find git root: %w", err)
			}
			mount.HostPath = root
		}

		if strings.HasPrefix(mount.HostPath, "~/") {
			mount.HostPath = filepath.Join(home, mount.HostPath[2:])
		} else if mount.HostPath == "~" {
			mount.HostPath = home
		}

		// TODO: Resolve this better once we find a container image where HOME is not /root
		if strings.HasPrefix(mount.SandboxPath, "~/") {
			mount.SandboxPath = "/root/" + mount.SandboxPath[2:]
		} else if mount.SandboxPath == "~" {
			mount.SandboxPath = "/root"
		}

		mount.HostPath = filepath.Clean(mount.HostPath)

		if mount.SandboxPath == "" {
			mount.SandboxPath = mount.HostPath
		}

		if err := validateMount(mount, originalHostPath, cacheDir); err != nil {
			return nil, err
		}

		resolved = append(resolved, mount)
		if isRepoRoot {
			gitMount := sandbox.Mount{
				HostPath:         filepath.Join(mount.HostPath, ".git"),
				SandboxPath:      filepath.Join(mount.SandboxPath, ".git"),
				ReadOnly:         true,
				WritableFallback: true,
			}
			if err := validateMount(gitMount, originalHostPath, cacheDir); err != nil {
				return nil, err
			}
			resolved = append(resolved, gitMount)
		}
	}
	return resolved, nil
}

func resolveScriptMounts(mounts []sandbox.Mount, sandboxType, image, scriptKey string) ([]sandbox.Mount, error) {
	imageSHA := ""
	needsSHA := false
	for _, mount := range mounts {
		if strings.Contains(mount.HostPath, "{cacheDir}") || strings.Contains(mount.HostPath, "${cacheDir}") {
			needsSHA = true
			break
		}
	}

	if needsSHA {
		var err error
		switch sandboxType {
		case "apple-container":
			imageSHA, err = getAppleContainerImageSHAFunction(image)
		case "proot":
			imageSHA, err = getProotImageSHAFunction(image)
		case "chroot":
			return nil, fmt.Errorf("mounts are not supported in chroot sandbox")
		default:
			imageSHA, err = getImageSHAFunction(image)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to get image SHA: %w", err)
		}
	}

	return resolveMounts(mounts, imageSHA, scriptKey)
}

func validateMount(mount sandbox.Mount, originalHostPath, cacheDir string) error {
	if cacheDir != "" {
		relativePath, err := filepath.Rel(cacheDir, mount.HostPath)
		if err != nil || !filepath.IsLocal(relativePath) {
			return fmt.Errorf("mount %q: host path escapes cache directory", originalHostPath)
		}
	}
	return nil
}

func findGitRoot(path string) (string, error) {
	dir, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for {
		gitEntry := filepath.Join(dir, ".git")
		if _, err := os.Lstat(gitEntry); err == nil {
			return dir, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%s: %w", gitEntry, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not a git repository (or any of the parent directories): .git")
		}
		dir = parent
	}
}

var getImageSHAFunction = getImageSHA

func getImageSHA(image string) (string, error) {
	log(2, "Getting SHA for image: %s", image)
	cmd := execCommand("docker", "images", "--no-trunc", "--quiet", image)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("error running docker images: %w", err)
	}
	sha := strings.TrimSpace(string(output))
	if sha == "" {
		log(1, "Image %s not found locally, pulling...", image)
		// Try pulling it
		pullCmd := execCommand("docker", "pull", image)
		pullCmd.Stdout = os.Stderr
		pullCmd.Stderr = os.Stderr
		if err := pullCmd.Run(); err != nil {
			return "", fmt.Errorf("failed to pull image %s: %w", image, err)
		}
		// Try again
		cmd = execCommand("docker", "images", "--no-trunc", "--quiet", image)
		output, err = cmd.Output()
		if err != nil {
			return "", fmt.Errorf("error running docker images after pull: %w", err)
		}
		sha = strings.TrimSpace(string(output))
	}

	if sha == "" {
		return "", fmt.Errorf("image still not found after pull: %s", image)
	}

	// sha is like "sha256:..."
	if strings.HasPrefix(sha, "sha256:") {
		sha = sha[7:]
	}
	log(2, "Image SHA for %s is %s", image, sha)
	return sha, nil
}

var getAppleContainerImageSHAFunction = getAppleContainerImageSHA

func getAppleContainerImageSHA(image string) (string, error) {
	// For apple/container we try to get the image digest using "inspect"
	cmd := execCommand("container", "image", "inspect", image)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("error running container image inspect: %w", err)
	}

	// The output is a JSON array.
	// We expect a descriptor or index with a digest.
	var info []struct {
		Descriptor struct {
			Digest string `json:"digest"`
		} `json:"descriptor"`
		Index struct {
			Digest string `json:"digest"`
		} `json:"index"`
	}

	if err := json.Unmarshal(output, &info); err != nil {
		// If unmarshalling fails, try to just return the image name as hash (not ideal but better than nothing)
		return "", fmt.Errorf("failed to parse container image inspect output: %w", err)
	}

	if len(info) == 0 {
		return "", fmt.Errorf("no image info found in container image inspect output")
	}

	digest := info[0].Descriptor.Digest
	if digest == "" {
		digest = info[0].Index.Digest
	}
	// Digest is likely like "sha256:..."
	if strings.HasPrefix(digest, "sha256:") {
		digest = digest[7:]
	}
	return digest, nil
}

var getProotImageSHAFunction = getProotImageSHA

func getProotImageSHA(imageRef string) (string, error) {
	image, err := crane.Pull(imageRef)
	if err != nil {
		return "", fmt.Errorf("pulling image %q: %w", imageRef, err)
	}
	digest, err := image.Digest()
	if err != nil {
		return "", fmt.Errorf("getting image digest: %w", err)
	}
	return digest.Hex, nil
}
