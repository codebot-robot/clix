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
	"fmt"
	"io"
	"path/filepath"
)

// ProotSandbox executes container workloads using user-space PRoot.
type ProotSandbox struct{}

var prepareRootFSFn = prepareRootFS

// Run executes the container image described by config using proot.
func (s *ProotSandbox) Run(stdin io.Reader, stdout, stderr io.Writer, config Config, args []string) error {
	var warnGit bool
	for _, m := range config.Mounts {
		if m.ReadOnly {
			if filepath.Base(m.HostPath) == ".git" {
				warnGit = true
			} else {
				return fmt.Errorf("read-only mounts are not supported by the proot sandbox")
			}
		}
	}

	rootPath := config.Image
	if rootPath == "" {
		return fmt.Errorf("ProotSandbox requires an image path (used as root directory)")
	}

	rootFS, err := prepareRootFSFn(rootPath)
	if err != nil {
		return err
	}
	defer rootFS.cleanup()

	// Determine the command to run
	var cmdPath string
	var cmdArgs []string

	if config.Entrypoint != "" {
		cmdPath = config.Entrypoint
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

	// proot -r realRoot [-b host:guest ...] cmdArgs
	prootArgs := []string{"-r", rootFS.dir}
	for _, m := range config.Mounts {
		validMount, err := validateMount(m)
		if err != nil {
			return err
		}
		prootArgs = append(prootArgs, "-b", fmt.Sprintf("%s:%s", validMount.HostPath, validMount.SandboxPath))
	}
	if warnGit {
		if _, err := fmt.Fprintf(stderr, "warning: proot sandbox cannot protect .git from writes\n"); err != nil {
			return err
		}
	}

	prootArgs = append(prootArgs, cmdArgs...)

	// Prepare the command
	cmd := ExecCommand("proot", prootArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	workDir := config.WorkingDir
	if workDir == "" {
		workDir = "/"
	}
	cmd.Dir = workDir

	cmd.Env = BuildEnv(rootFS.env, config.Env)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error running proot command: %w", err)
	}

	return nil
}
