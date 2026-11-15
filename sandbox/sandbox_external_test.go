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

package sandbox_test

import (
	"strings"
	"testing"

	"github.com/gke-labs/clix/sandbox"
)

func TestExternalUsage(t *testing.T) {
	t.Setenv("CLIX_SANDBOX", "docker")

	selectedSandbox, err := sandbox.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv failed: %v", err)
	}

	config := sandbox.Config{
		Image:      "alpine:latest",
		Entrypoint: "/bin/echo",
		Mounts: []sandbox.Mount{
			{HostPath: "/tmp", SandboxPath: "/tmp", ReadOnly: true},
		},
		Env: []sandbox.EnvVar{
			{Name: "HELLO", Value: "world"},
		},
		WorkingDir: "/tmp",
	}

	if config.Image != "alpine:latest" {
		t.Errorf("unexpected image: %s", config.Image)
	}

	_ = selectedSandbox
}

func TestExternalBackends(t *testing.T) {
	backends := []string{"docker", "apple-container", "chroot", "proot"}
	for _, backend := range backends {
		selectedSandbox, err := sandbox.New(backend)
		if err != nil {
			t.Errorf("New(%q) failed: %v", backend, err)
		}
		if selectedSandbox == nil {
			t.Errorf("New(%q) returned nil", backend)
		}
	}

	_, err := sandbox.New("nonexistent")
	if err == nil {
		t.Errorf("expected error for nonexistent backend")
	} else if !strings.Contains(err.Error(), "unknown sandbox backend") {
		t.Errorf("unexpected error message: %v", err)
	}
}
