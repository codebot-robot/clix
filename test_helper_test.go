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
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/gke-labs/clix/sandbox"
)

type fakeHelperSandbox struct{}

func (s *fakeHelperSandbox) Usable(ctx context.Context) error {
	return nil
}

func (s *fakeHelperSandbox) String() string {
	return "fake"
}

func (s *fakeHelperSandbox) Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, script sandbox.Config, args []string) error {
	cmd := execCommand("docker", args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return sandbox.NewExitError(exitError.ExitCode())
		}
		return err
	}
	return nil
}

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

	cmd, cmdArgs := args[0], args[1:]

	behavior := os.Getenv("MOCK_BEHAVIOR")
	if behavior == "exit_42" {
		os.Exit(42)
	}
	if behavior == "fail_git_clone" && cmd == "git" && len(cmdArgs) >= 1 && cmdArgs[0] == "clone" {
		fmt.Fprintf(os.Stderr, "fatal: repository not found\n")
		os.Exit(128)
	}

	switch cmd {
	case "git":
		if len(cmdArgs) >= 2 && cmdArgs[0] == "ls-remote" {
			// Mock ls-remote: return a dummy 40-character hash
			ref := "refs/heads/main"
			for _, arg := range cmdArgs[1:] {
				if strings.HasPrefix(arg, "refs/heads/") || strings.HasPrefix(arg, "refs/tags/") {
					ref = arg
					break
				}
			}
			fmt.Printf("0123456789abcdef0123456789abcdef01234567\t%s\n", ref)
			os.Exit(0)
		}
		if len(cmdArgs) >= 1 && cmdArgs[0] == "clone" {
			// Mock clone: success
			fmt.Fprintf(os.Stderr, "Mock cloning...\n")
			os.Exit(0)
		}
	case "docker":
		if len(cmdArgs) >= 2 && cmdArgs[0] == "images" && cmdArgs[1] == "-q" {
			if behavior == "image_exists" {
				fmt.Printf("image-id\n")
			}
			// else empty output
			os.Exit(0)
		}
		if len(cmdArgs) >= 2 && cmdArgs[0] == "buildx" {
			// Mock build: success
			fmt.Fprintf(os.Stderr, "Mock building...\n")
			os.Exit(0)
		}
	case "container":
		if len(cmdArgs) >= 3 && cmdArgs[0] == "image" && cmdArgs[1] == "inspect" {
			fmt.Printf(`[{"descriptor": {"digest": "sha256:abcdef123456"}}]`)
			os.Exit(0)
		}
		if len(cmdArgs) >= 3 && cmdArgs[0] == "image" && cmdArgs[1] == "list" {
			if behavior == "image_exists" {
				fmt.Printf("image-id\n")
			}
			os.Exit(0)
		}
		if len(cmdArgs) >= 1 && cmdArgs[0] == "build" {
			fmt.Fprintf(os.Stderr, "Mock container building...\n")
			os.Exit(0)
		}
	}
	os.Exit(0)
}

func TestHelperMainProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MAIN_HELPER_PROCESS") != "1" {
		return
	}
	execCommand = fakeExecCommand
	newSandbox = func(string) (sandbox.Sandbox, string, error) { return &fakeHelperSandbox{}, "docker", nil }
	testBinary := os.Args[0]
	var subArgs []string
	for i, arg := range os.Args {
		if arg == "--" {
			subArgs = os.Args[i+1:]
			break
		}
	}
	os.Args = append([]string{testBinary}, subArgs...)
	main()
}
