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

func TestBuildDockerArgs(t *testing.T) {
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

	cmdArgs, err := buildDockerArgs(config, args, false)
	if err != nil {
		t.Fatalf("buildDockerArgs failed: %v", err)
	}

	joinedArgs := strings.Join(cmdArgs, " ")

	expectedTokens := []string{
		"run --rm -i",
		"-v /tmp/data:/data",
		"-v /tmp/config:/etc/app:ro",
		"-e MY_VAR=hello",
		"-w /workspace",
		"--entrypoint /bin/sh",
		"python:3.11 -c echo test",
	}

	for _, token := range expectedTokens {
		if !strings.Contains(joinedArgs, token) {
			t.Errorf("expected docker args to contain %q, got: %s", token, joinedArgs)
		}
	}

	terminalArgs, err := buildDockerArgs(config, args, true)
	if err != nil {
		t.Fatalf("buildDockerArgs with isTerm=true failed: %v", err)
	}
	if len(terminalArgs) < 4 || terminalArgs[0] != "run" || terminalArgs[1] != "--rm" || terminalArgs[2] != "-i" || terminalArgs[3] != "-t" {
		t.Errorf("expected terminal args to start with [run, --rm, -i, -t], got %v", terminalArgs)
	}
}
