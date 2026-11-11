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
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

type DockerSandbox struct{}

func (s *DockerSandbox) Run(stdin io.Reader, stdout, stderr io.Writer, script Script, args []string) error {
	log(2, "DockerSandbox: preparing args")
	cmdArgs, err := buildDockerArgs(script, args, isTerminal(stdin))
	if err != nil {
		return fmt.Errorf("error building docker args: %w", err)
	}

	log(1, "DockerSandbox: running docker %v", cmdArgs)
	cmd := execCommand("docker", cmdArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Propagate the exit code from the subcommand
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("error running docker command: %w", err)
	}
	return nil
}

func buildDockerArgs(script Script, args []string, isTerm bool) ([]string, error) {
	cmdArgs := []string{"run", "-i"}
	if isTerm {
		cmdArgs = append(cmdArgs, "-t")
	}

	// Resolve cache directory if needed
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
		imageSHA, err = getImageSHAFn(script.Image)
		if err != nil {
			return nil, fmt.Errorf("failed to get image SHA: %w", err)
		}
	}

	resolvedMounts, err := resolveMounts(script.Mounts, imageSHA)
	if err != nil {
		return nil, fmt.Errorf("error resolving mounts: %w", err)
	}

	for _, m := range resolvedMounts {
		v := fmt.Sprintf("%s:%s", m.HostPath, m.SandboxPath)
		if m.ReadOnly {
			v += ":ro"
		}
		cmdArgs = append(cmdArgs, "-v", v)
	}

	for _, e := range script.Env {
		cmdArgs = append(cmdArgs, "-e", fmt.Sprintf("%s=%s", e.Name, e.Value))
	}

	// Set working directory to CWD if possible
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("error getting current working directory: %w", err)
	}
	cmdArgs = append(cmdArgs, "-w", cwd)

	if script.Entrypoint != "" {
		cmdArgs = append(cmdArgs, "--entrypoint", script.Entrypoint)
	}
	cmdArgs = append(cmdArgs, script.Image)
	cmdArgs = append(cmdArgs, args...)

	return cmdArgs, nil
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

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
