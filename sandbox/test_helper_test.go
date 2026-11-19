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
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// fakeExecCommand mocks exec.Command for testing.
func fakeExecCommand(command string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestHelperProcess", "--", command}
	cs = append(cs, args...)
	cmd := exec.Command(os.Args[0], cs...)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	return cmd
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "No command\n")
		os.Exit(2)
	}

	cmd, _ := args[0], args[1:]
	baseCommand := filepath.Base(cmd)

	behavior := os.Getenv("MOCK_BEHAVIOR")
	if behavior == "exit_42" {
		os.Exit(42)
	}
	if behavior == "docker_info_error" {
		if (cmd == "docker" || baseCommand == "docker") && len(args) > 1 && args[1] == "info" {
			fmt.Fprintf(os.Stderr, "Cannot connect to the Docker daemon. Is the docker daemon running?\n")
			os.Exit(1)
		}
	}
	if behavior == "container_service_error" {
		if (cmd == "container" || baseCommand == "container") && len(args) > 2 && args[1] == "system" && args[2] == "status" {
			fmt.Fprintf(os.Stderr, "apiserver is not running and not registered with launchd\n")
			os.Exit(1)
		}
	}
	if behavior == "sleep" {
		if (cmd == "docker" || baseCommand == "docker") && len(args) > 1 && args[1] == "rm" {
			os.Exit(0)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	if behavior == "docker_rm_error" {
		if (cmd == "docker" || baseCommand == "docker") && len(args) > 1 && args[1] == "rm" {
			fmt.Fprintf(os.Stderr, "daemon error: cannot stop container\n")
			os.Exit(1)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	if behavior == "docker_rm_hang" {
		if (cmd == "docker" || baseCommand == "docker") && len(args) > 1 && args[1] == "rm" {
			time.Sleep(30 * time.Second)
			os.Exit(0)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	if behavior == "spawn_grandchild" {
		grandchild := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", "grandchild")
		grandchild.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "MOCK_BEHAVIOR=sleep")
		if err := grandchild.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to start grandchild: %v\n", err)
			os.Exit(1)
		}
		pidFile := os.Getenv("GRANDCHILD_PID_FILE")
		if pidFile != "" {
			_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", grandchild.Process.Pid)), 0644)
		}
		_ = grandchild.Wait()
		os.Exit(0)
	}

	switch cmd {
	case "docker":
		os.Exit(0)
	case "container":
		os.Exit(0)
	case "proot":
		os.Exit(0)
	}
	os.Exit(0)
}
