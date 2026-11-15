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
	"strings"
	"testing"
)

func TestBuildAppleContainerArgs(t *testing.T) {
	config := Config{
		Image:      "python:3.11",
		Entrypoint: "/bin/sh",
		Mounts: []Mount{
			{HostPath: "/tmp/data", SandboxPath: "/data", ReadOnly: false},
			{HostPath: "/tmp/config", SandboxPath: "/etc/app", ReadOnly: true},
		},
		Env: []EnvVar{
			{Name: "MY_VAR", Value: "hello"},
		},
		WorkingDir: "/workspace",
	}
	args := []string{"-c", "echo test"}

	cmdArgs, err := buildAppleContainerArgs(config, args, false)
	if err != nil {
		t.Fatalf("buildAppleContainerArgs failed: %v", err)
	}

	joinedArgs := strings.Join(cmdArgs, " ")

	expectedTokens := []string{
		"run --rm",
		"--mount type=bind,source=/tmp/data,target=/data",
		"--mount type=bind,source=/tmp/config,target=/etc/app,readonly",
		"-e MY_VAR=hello",
		"-w /workspace",
		"--entrypoint /bin/sh",
		"python:3.11 -c echo test",
	}

	for _, token := range expectedTokens {
		if !strings.Contains(joinedArgs, token) {
			t.Errorf("expected apple container args to contain %q, got: %s", token, joinedArgs)
		}
	}

	terminalArgs, err := buildAppleContainerArgs(config, args, true)
	if err != nil {
		t.Fatalf("buildAppleContainerArgs with isTerm=true failed: %v", err)
	}
	if len(terminalArgs) < 3 || terminalArgs[0] != "run" || terminalArgs[1] != "--rm" || terminalArgs[2] != "-it" {
		t.Errorf("expected terminal args to start with [run, --rm, -it], got %v", terminalArgs)
	}
}
