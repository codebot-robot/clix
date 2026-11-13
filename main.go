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
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

var execCommand = exec.Command

func log(level int, format string, v ...any) {
	verbosity := 0
	if vStr := os.Getenv("CLIX_LOG_VERBOSITY"); vStr != "" {
		fmt.Sscanf(vStr, "%d", &verbosity)
	}
	if verbosity >= level {
		fmt.Fprintf(os.Stderr, "clix: "+format+"\n", v...)
	}
}

type Script struct {
	Go         *GoConfig    `json:"go,omitempty"`
	Build      *BuildConfig `json:"build,omitempty"`
	Image      string       `json:"image,omitempty"`
	Entrypoint string       `json:"entrypoint,omitempty"`
	Mounts     []Mount      `json:"mounts,omitempty"`
	Env        []EnvVar     `json:"env,omitempty"`
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

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Mount struct {
	HostPath    string `json:"hostPath"`
	SandboxPath string `json:"sandboxPath,omitempty"`
}

type GoConfig struct {
	Run     string `json:"run"`
	Version string `json:"version,omitempty"`
}

func main() {
	if err := run(os.Stdin, os.Stdout, os.Stderr, os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
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
	if err := yaml.Unmarshal(data, &script); err != nil {
		return fmt.Errorf("error parsing script file: %w", err)
	}

	if script.Build != nil {
		imageName, err := buildImage(stdin, stdout, stderr, script.Build, scriptPath)
		if err != nil {
			return fmt.Errorf("error building image: %w", err)
		}
		script.Image = imageName
	}

	var sandbox Sandbox
	sandboxType := os.Getenv("CLIX_SANDBOX")
	switch sandboxType {
	case "chroot":
		sandbox = &ChrootSandbox{}
	case "proot":
		sandbox = &ProotSandbox{}
	case "apple-container":
		sandbox = &AppleContainerSandbox{}
	default:
		sandboxType = "docker"
		sandbox = &DockerSandbox{}
	}
	log(1, "Using sandbox: %s", sandboxType)

	if script.Image != "" {
		log(1, "Running image: %s", script.Image)
		return sandbox.Run(stdin, stdout, stderr, script, scriptArgs)
	}

	if script.Go != nil {
		if len(script.Mounts) > 0 {
			log(1, "Script has mounts, transforming into Docker script")
			// Transform into a Docker script
			script.Image = "golang:latest"

			// Add cache mounts for Go to speed up subsequent runs
			script.Mounts = append(script.Mounts, Mount{
				HostPath:    "${cacheDir}/gopath",
				SandboxPath: "/go",
			})
			script.Mounts = append(script.Mounts, Mount{
				HostPath:    "${cacheDir}/cache",
				SandboxPath: "/root/.cache",
			})

			// We need to construct the command arguments for `go run ...`
			goPackage := script.Go.Run
			if script.Go.Version != "" {
				goPackage = fmt.Sprintf("%s@%s", goPackage, script.Go.Version)
			}
			// Prepend "go", "run", goPackage to the user arguments
			// Note: We don't set Entrypoint because runDocker appends Image then Args.
			// So `docker run ... golang:latest go run pkg args...` works.
			newArgs := append([]string{"go", "run", goPackage}, scriptArgs...)
			log(1, "Transformed command: go run %s", goPackage)
			return sandbox.Run(stdin, stdout, stderr, script, newArgs)
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
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Propagate the exit code from the subcommand
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("error running command: %w", err)
	}

	return nil
}

func buildImage(stdin io.Reader, stdout, stderr io.Writer, build *BuildConfig, scriptName string) (string, error) {
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
	defer os.RemoveAll(tempDir)

	// Clone
	cloneArgs := []string{"clone", "--depth", "1"}
	if build.Branch != "" {
		cloneArgs = append(cloneArgs, "--branch", build.Branch)
	}
	cloneArgs = append(cloneArgs, "--", build.Git, tempDir)

	fmt.Fprintf(stderr, "Cloning %s...\n", build.Git)
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
		// Use standard 'docker build' for better compatibility than 'buildx'
		buildArgs = []string{"build", "-f", dockerfile, "-t", imageTag, "."}
	}

	fmt.Fprintf(stderr, "Building image %s...\n", imageTag)
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
