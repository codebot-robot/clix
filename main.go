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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/clix/sandbox"
	"sigs.k8s.io/yaml"
)

var execCommand = exec.Command

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

// Mount is an alias for sandbox.Mount.
type Mount = sandbox.Mount

// EnvVar is an alias for sandbox.EnvVar.
type EnvVar = sandbox.EnvVar

type Script struct {
	Go         *GoConfig        `json:"go,omitempty"`
	Build      *BuildConfig     `json:"build,omitempty"`
	Image      string           `json:"image,omitempty"`
	Entrypoint string           `json:"entrypoint,omitempty"`
	Mounts     []sandbox.Mount  `json:"mounts,omitempty"`
	Env        []sandbox.EnvVar `json:"env,omitempty"`

	key string
}

// BuildConfig allows building an image from source code
type BuildConfig struct {
	// Git is the repo URL we should clone to get the source code
	Git string `json:"git"`
	// Branch is the branch (or tag) we should clone. Defaults to the default branch
	Branch string `json:"branch,omitempty"`
	// Dockerfile is the path to the Dockerfile, relative to the git repo root
	Dockerfile string `json:"dockerfile,omitempty"`
}

type GoConfig struct {
	Run     string `json:"run"`
	Version string `json:"version,omitempty"`
}

func main() {
	if err := run(os.Stdin, os.Stdout, os.Stderr, os.Args); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			os.Exit(exitError.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
	sandbox.ExecCommand = execCommand
	if len(args) < 2 {
		return fmt.Errorf("usage: %s <script> [args...]", args[0])
	}

	scriptPath := args[1]
	scriptArgs := args[2:]

	data, err := os.ReadFile(scriptPath)
	if err != nil {
		return fmt.Errorf("error reading script file: %w", err)
	}

	var script Script
	if err := yaml.UnmarshalStrict(data, &script); err != nil {
		return fmt.Errorf("error parsing script file: %w", err)
	}

	absPath, err := filepath.Abs(scriptPath)
	if err != nil {
		return fmt.Errorf("error resolving script path: %w", err)
	}
	h := sha256.Sum256([]byte(absPath))
	script.key = hex.EncodeToString(h[:])[:16]

	if script.Build != nil {
		imageName, err := buildImage(stdin, stdout, stderr, script.Build, scriptPath)
		if err != nil {
			return fmt.Errorf("error building image: %w", err)
		}
		script.Image = imageName
	}

	sandboxType := os.Getenv("CLIX_SANDBOX")
	if sandboxType == "" {
		sandboxType = "docker"
	}
	selectedSandbox, err := sandbox.New(sandboxType)
	if err != nil {
		return err
	}
	log(1, "Using sandbox: %s", sandboxType)

	if script.Image != "" {
		log(1, "Running image: %s", script.Image)
		imageSHA := ""
		needsSHA := false
		for _, m := range script.Mounts {
			if strings.Contains(m.HostPath, "{cacheDir}") || strings.Contains(m.HostPath, "${cacheDir}") {
				needsSHA = true
				break
			}
		}

		if needsSHA {
			var err error
			imageSHA, err = resolveImageSHAForBackend(sandboxType, script.Image)
			if err != nil {
				return fmt.Errorf("failed to get image SHA: %w", err)
			}
		}

		resolvedMounts, err := resolveMounts(script.Mounts, imageSHA, script.key)
		if err != nil {
			return fmt.Errorf("error resolving mounts: %w", err)
		}

		config := sandbox.Config{
			Image:      script.Image,
			Entrypoint: script.Entrypoint,
			Mounts:     resolvedMounts,
			Env:        script.Env,
		}
		return selectedSandbox.Run(stdin, stdout, stderr, config, scriptArgs)
	}

	if script.Go != nil {
		if len(script.Mounts) > 0 {
			log(1, "Script has mounts, transforming into Docker script")
			// Transform into a Docker script
			script.Image = "golang:latest"

			// Add cache mounts for Go to speed up subsequent runs
			script.Mounts = append(script.Mounts, sandbox.Mount{
				HostPath:    "${cacheDir}/gopath",
				SandboxPath: "/go",
			})
			script.Mounts = append(script.Mounts, sandbox.Mount{
				HostPath:    "${cacheDir}/cache",
				SandboxPath: "/root/.cache",
			})

			// We need to construct the command arguments for `go run ...`
			goPackage := script.Go.Run
			if script.Go.Version != "" {
				goPackage = fmt.Sprintf("%s@%s", goPackage, script.Go.Version)
			}
			newArgs := append([]string{"go", "run", goPackage}, scriptArgs...)
			log(1, "Transformed command: go run %s", goPackage)

			imageSHA, err := resolveImageSHAForBackend(sandboxType, script.Image)
			if err != nil {
				return fmt.Errorf("failed to get image SHA: %w", err)
			}

			resolvedMounts, err := resolveMounts(script.Mounts, imageSHA, script.key)
			if err != nil {
				return fmt.Errorf("error resolving mounts: %w", err)
			}

			config := sandbox.Config{
				Image:  script.Image,
				Mounts: resolvedMounts,
				Env:    script.Env,
			}
			return selectedSandbox.Run(stdin, stdout, stderr, config, newArgs)
		}
		log(1, "Running go run: %s", script.Go.Run)
		return runGo(stdin, stdout, stderr, script.Go, scriptArgs)
	}

	return fmt.Errorf("error: script configuration missing (expected 'go' or 'image')")
}

func runGo(stdin io.Reader, stdout, stderr io.Writer, config *GoConfig, args []string) error {
	goPackage := config.Run
	version := config.Version

	if goPackage == "" {
		return fmt.Errorf("error: 'go.run' missing in script")
	}

	target := goPackage
	if version != "" {
		target = fmt.Sprintf("%s@%s", goPackage, version)
	}

	log(1, "Running go run %s", target)
	cmdArgs := append([]string{"run", target}, args...)
	cmd := execCommand("go", cmdArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error running command: %w", err)
	}

	return nil
}

func buildImage(stdin io.Reader, stdout, stderr io.Writer, build *BuildConfig, scriptName string) (_ string, retErr error) {
	if build.Git == "" {
		return "", fmt.Errorf("build.git is required")
	}
	if strings.HasPrefix(build.Git, "-") {
		return "", fmt.Errorf("build.git cannot start with '-'")
	}

	log(1, "Building image from %s", build.Git)

	// Get the latest commit hash from the remote
	commitHash, err := getRemoteHead(build.Git, build.Branch)
	if err != nil {
		return "", fmt.Errorf("failed to get remote head: %w", err)
	}
	log(2, "Remote head is %s", commitHash)

	// Construct image tag: clix-<script-name>-<hash-of-repo-url>:<commit-hash>
	repoHash := sha256.Sum256([]byte(build.Git))
	repoHashStr := hex.EncodeToString(repoHash[:])[:8] // Short hash for readability

	absPath, err := filepath.Abs(scriptName)
	if err != nil {
		absPath = scriptName // Fallback
	}
	scriptHash := sha256.Sum256([]byte(absPath))
	scriptHashStr := hex.EncodeToString(scriptHash[:])[:8]

	baseName := filepath.Base(scriptName)
	baseName = strings.TrimSuffix(baseName, filepath.Ext(baseName))
	baseName = strings.ReplaceAll(baseName, ":", "-")
	baseName = strings.ToLower(baseName)

	imageTag := fmt.Sprintf("clix-%s-%s-%s:%s", baseName, scriptHashStr, repoHashStr, commitHash)
	log(1, "Generated image tag: %s", imageTag)

	// Check if image exists
	exists, err := imageExists(imageTag)
	if err != nil {
		return "", fmt.Errorf("failed to check if image exists: %w", err)
	}

	if exists {
		log(1, "Image %s already exists, using cache", imageTag)
		return imageTag, nil
	}

	log(1, "Image %s not found, building...", imageTag)

	// Clone and build
	tempDir, err := os.MkdirTemp("", "clix-build-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("failed to remove temp dir: %w", err))
		}
	}()

	// Clone
	cloneArgs := []string{"clone", "--depth", "1"}
	if build.Branch != "" {
		cloneArgs = append(cloneArgs, "--branch", build.Branch)
	}
	cloneArgs = append(cloneArgs, "--", build.Git, tempDir)

	if _, err := fmt.Fprintf(stderr, "Cloning %s...\n", build.Git); err != nil {
		return "", err
	}
	cmd := execCommand("git", cloneArgs...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git clone failed: %w", err)
	}

	// Build
	dockerfile := "Dockerfile"
	if build.Dockerfile != "" {
		dockerfile = build.Dockerfile
	}

	var buildCmd string
	var buildArgs []string

	if os.Getenv("CLIX_SANDBOX") == "apple-container" {
		buildCmd = "container"
		buildArgs = []string{"build", "-t", imageTag, "-f", dockerfile, "."}
	} else {
		buildCmd = "docker"
		buildArgs = []string{"build", "-f", dockerfile, "-t", imageTag, "."}
	}

	if _, err := fmt.Fprintf(stderr, "Building image %s...\n", imageTag); err != nil {
		return "", err
	}
	cmd = execCommand(buildCmd, buildArgs...)
	cmd.Dir = tempDir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s build failed: %w", buildCmd, err)
	}

	return imageTag, nil
}

func getRemoteHead(repo, branch string) (string, error) {
	log(2, "Getting remote head for %s (branch: %s)", repo, branch)
	args := []string{"ls-remote", "--", repo}
	if branch != "" {
		args = append(args, "refs/heads/"+branch, "refs/tags/"+branch)
	} else {
		args = append(args, "HEAD")
	}

	cmd := execCommand("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var selectedHash string
	if branch != "" {
		var tagHash string
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			if fields[1] == "refs/heads/"+branch {
				selectedHash = fields[0]
				break
			}
			if fields[1] == "refs/tags/"+branch && tagHash == "" {
				tagHash = fields[0]
			}
		}
		if selectedHash == "" {
			selectedHash = tagHash
		}
		if selectedHash == "" {
			return "", fmt.Errorf("branch %q not found", branch)
		}
	} else {
		if len(lines) == 0 || lines[0] == "" {
			return "", fmt.Errorf("no output from git ls-remote")
		}
		fields := strings.Fields(lines[0])
		if len(fields) == 0 {
			return "", fmt.Errorf("could not parse git ls-remote output")
		}
		selectedHash = fields[0]
	}

	if len(selectedHash) != 40 && len(selectedHash) != 64 {
		return "", fmt.Errorf("invalid commit hash %q: must be 40 or 64 hexadecimal characters", selectedHash)
	}
	if _, err := hex.DecodeString(selectedHash); err != nil {
		return "", fmt.Errorf("invalid commit hash %q: %w", selectedHash, err)
	}

	log(2, "Remote head for %s is %s", repo, selectedHash)
	return selectedHash, nil
}

func imageExists(tag string) (bool, error) {
	cmdName := "docker"
	args := []string{"images", "-q", tag}
	if os.Getenv("CLIX_SANDBOX") == "apple-container" {
		cmdName = "container"
		args = []string{"image", "list", tag}
	}

	log(2, "Checking if image exists: %s %v", cmdName, args)
	cmd := execCommand(cmdName, args...)
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	exists := strings.TrimSpace(string(out)) != ""
	log(2, "Image exists (%s): %v", tag, exists)
	return exists, nil
}

func resolveImageSHAForBackend(sandboxType, image string) (string, error) {
	if sandboxType == "apple-container" {
		return getAppleContainerImageSHAFn(image)
	}
	return getImageSHAFn(image)
}

var getImageSHAFn = getImageSHA

func getImageSHA(image string) (string, error) {
	log(2, "Getting SHA for image: %s", image)
	cmd := execCommand("docker", "images", "--no-trunc", "--quiet", image)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("error running docker images: %w", err)
	}
	sha := strings.TrimSpace(string(out))
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
		out, err = cmd.Output()
		if err != nil {
			return "", fmt.Errorf("error running docker images after pull: %w", err)
		}
		sha = strings.TrimSpace(string(out))
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

var getAppleContainerImageSHAFn = getAppleContainerImageSHA

func getAppleContainerImageSHA(image string) (string, error) {
	cmd := execCommand("container", "image", "inspect", image)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("error running container image inspect: %w", err)
	}

	var info []struct {
		Descriptor struct {
			Digest string `json:"digest"`
		} `json:"descriptor"`
		Index struct {
			Digest string `json:"digest"`
		} `json:"index"`
	}

	if err := json.Unmarshal(out, &info); err != nil {
		return "", fmt.Errorf("failed to parse container image inspect output: %w", err)
	}

	if len(info) == 0 {
		return "", fmt.Errorf("no image info found in container image inspect output")
	}

	digest := info[0].Descriptor.Digest
	if digest == "" {
		digest = info[0].Index.Digest
	}
	if strings.HasPrefix(digest, "sha256:") {
		digest = digest[7:]
	}
	return digest, nil
}

func resolveMounts(mounts []sandbox.Mount, imageSHA, scriptKey string) ([]sandbox.Mount, error) {
	var resolved []sandbox.Mount
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user home dir: %w", err)
	}

	for _, m := range mounts {
		originalHostPath := m.HostPath
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

		isRepoRoot := m.HostPath == "git.repoRoot(cwd)"
		if isRepoRoot {
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

		if err := validateScriptMount(m, originalHostPath, cacheDir); err != nil {
			return nil, err
		}

		resolved = append(resolved, m)
		if isRepoRoot {
			gitMount := sandbox.Mount{
				HostPath:    filepath.Join(m.HostPath, ".git"),
				SandboxPath: filepath.Join(m.SandboxPath, ".git"),
				ReadOnly:    true,
			}
			if err := validateScriptMount(gitMount, originalHostPath, cacheDir); err != nil {
				return nil, err
			}
			resolved = append(resolved, gitMount)
		}
	}
	return resolved, nil
}

func validateScriptMount(m sandbox.Mount, originalHostPath, cacheDir string) error {
	if !filepath.IsAbs(m.HostPath) {
		return fmt.Errorf("mount %q: host path is not absolute", originalHostPath)
	}
	if cacheDir != "" {
		rel, err := filepath.Rel(cacheDir, m.HostPath)
		if err != nil || !filepath.IsLocal(rel) {
			return fmt.Errorf("mount %q: host path escapes cache directory", originalHostPath)
		}
	}
	if !filepath.IsAbs(m.SandboxPath) {
		return fmt.Errorf("mount %q: sandbox path %q is not absolute", originalHostPath, m.SandboxPath)
	}
	if strings.Contains(m.HostPath, ":") {
		return fmt.Errorf("mount %q: host path contains ':'", originalHostPath)
	}
	if strings.Contains(m.SandboxPath, ":") {
		return fmt.Errorf("mount %q: sandbox path %q contains ':'", originalHostPath, m.SandboxPath)
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
