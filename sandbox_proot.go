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
)

type ProotSandbox struct{}

func (s *ProotSandbox) Run(stdin io.Reader, stdout, stderr io.Writer, script Script, args []string) error {
	rootPath := script.Image
	if rootPath == "" {
		return fmt.Errorf("ProotSandbox requires an image path (used as root directory)")
	}

	rootFS, err := prepareRootFS(rootPath)
	if err != nil {
		return err
	}
	defer rootFS.cleanup()

	// Determine the command to run
	var cmdPath string
	var cmdArgs []string

	if script.Entrypoint != "" {
		cmdPath = script.Entrypoint
		cmdArgs = append([]string{cmdPath}, args...)
	} else {
		// If no entrypoint, use the first argument as command
		if len(args) > 0 {
			cmdPath = args[0]
			cmdArgs = args
		} else {
			return fmt.Errorf("no command specified and no entrypoint in script")
		}
	}

	resolvedMounts, err := resolveMounts(script.Mounts, rootFS.imageSHA, script.key)
	if err != nil {
		return fmt.Errorf("error resolving mounts: %w", err)
	}

	// proot -r realRoot [-b host:guest ...] cmdArgs
	prootArgs := []string{"-r", rootFS.dir}
	for _, m := range resolvedMounts {
		prootArgs = append(prootArgs, "-b", fmt.Sprintf("%s:%s", m.HostPath, m.SandboxPath))
	}

	prootArgs = append(prootArgs, cmdArgs...)

	// Prepare the command
	cmd := execCommand("proot", prootArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// We start at root of the new root
	cmd.Dir = "/"

	cmd.Env = buildSandboxEnv(rootFS.env, script.Env)

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("error running proot command: %w", err)
	}

	return nil
}
