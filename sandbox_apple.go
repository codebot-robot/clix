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
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type AppleContainerSandbox struct{}

func (s *AppleContainerSandbox) Run(stdin io.Reader, stdout, stderr io.Writer, script Script, args []string) error {
	cmdArgs, err := buildAppleContainerArgs(script, args, isTerminal(stdin))
	if err != nil {
		return fmt.Errorf("error building apple/container args: %w", err)
	}

	cmd := execCommand("container", cmdArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Propagate the exit code from the subcommand
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("error running container command: %w", err)
	}
	return nil
}

func buildAppleContainerArgs(script Script, args []string, isTerm bool) ([]string, error) {
	cmdArgs := []string{"run", "--rm"}
	if isTerm {
		cmdArgs = append(cmdArgs, "-it")
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
		imageSHA, err = getAppleContainerImageSHAFn(script.Image)
		if err != nil {
			return nil, fmt.Errorf("failed to get image SHA: %w", err)
		}
	}

	resolvedMounts, err := resolveMounts(script.Mounts, imageSHA)
	if err != nil {
		return nil, fmt.Errorf("error resolving mounts: %w", err)
	}

	for _, m := range resolvedMounts {
		if m.ReadOnly {
			cmdArgs = append(cmdArgs, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s,readonly", m.HostPath, m.SandboxPath))
		} else {
			cmdArgs = append(cmdArgs, "-v", fmt.Sprintf("%s:%s", m.HostPath, m.SandboxPath))
		}
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

var getAppleContainerImageSHAFn = getAppleContainerImageSHA

func getAppleContainerImageSHA(image string) (string, error) {
	// For apple/container we try to get the image digest using "inspect"
	cmd := execCommand("container", "image", "inspect", image)
	out, err := cmd.Output()
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

	if err := json.Unmarshal(out, &info); err != nil {
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
