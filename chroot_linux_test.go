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
	"archive/tar"
	"bytes"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	empty "github.com/google/go-containerregistry/pkg/v1/empty"
	mutate "github.com/google/go-containerregistry/pkg/v1/mutate"
	tarball "github.com/google/go-containerregistry/pkg/v1/tarball"
)

func TestRunChroot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("skipping chroot test: not root")
	}

	os.Setenv("CLIX_SANDBOX", "chroot")
	defer os.Unsetenv("CLIX_SANDBOX")

	t.Run("HelloWorld", func(t *testing.T) {
		tmpDir := t.TempDir()
		scriptPath := filepath.Join(tmpDir, "test-script-chroot-pull")

		// hello-world binary is at /hello
		scriptContent := `#!/usr/bin/env clix
image: hello-world
entrypoint: /hello
`

		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("Failed to write script: %v", err)
		}

		var stdout, stderr bytes.Buffer
		stdin := strings.NewReader("")
		args := []string{"clix", scriptPath}

		err := run(stdin, &stdout, &stderr, args)
		if err != nil {
			t.Fatalf("run failed: %v", err)
		}

		if !strings.Contains(stdout.String(), "Hello from Docker!") {
			t.Errorf("unexpected output: %q", stdout.String())
		}
	})

	t.Run("PID", func(t *testing.T) {
		// Build a minimal binary that prints its own PID.
		tmpDir := t.TempDir()
		pidSrc := filepath.Join(tmpDir, "main.go")
		src := `package main
import ("fmt"; "os")
func main() { fmt.Println(os.Getpid()) }
`
		if err := os.WriteFile(pidSrc, []byte(src), 0644); err != nil {
			t.Fatalf("failed to write pid source: %v", err)
		}

		pidBin := filepath.Join(tmpDir, "printpid")
		buildCmd := exec.Command("go", "build", "-o", pidBin, pidSrc)
		buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
		if out, err := buildCmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to build pid binary: %v\noutput: %s", err, out)
		}

		binData, err := os.ReadFile(pidBin)
		if err != nil {
			t.Fatalf("failed to read compiled binary: %v", err)
		}

		// Create a tar layer containing /printpid
		var tarBuf bytes.Buffer
		tw := tar.NewWriter(&tarBuf)
		hdr := &tar.Header{
			Name: "printpid",
			Mode: 0755,
			Size: int64(len(binData)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("failed to write tar header: %v", err)
		}
		if _, err := tw.Write(binData); err != nil {
			t.Fatalf("failed to write tar body: %v", err)
		}
		if err := tw.Close(); err != nil {
			t.Fatalf("failed to close tar writer: %v", err)
		}

		layer, err := tarball.LayerFromReader(&tarBuf)
		if err != nil {
			t.Fatalf("failed to create layer: %v", err)
		}

		img, err := mutate.AppendLayers(empty.Image, layer)
		if err != nil {
			t.Fatalf("failed to append layer: %v", err)
		}

		s := httptest.NewServer(registry.New())
		defer s.Close()

		u, err := url.Parse(s.URL)
		if err != nil {
			t.Fatalf("failed to parse registry URL: %v", err)
		}

		repo := u.Host + "/pid-test:latest"
		if err := crane.Push(img, repo); err != nil {
			t.Fatalf("failed to push image to local registry: %v", err)
		}

		scriptPath := filepath.Join(tmpDir, "test-script-chroot-pid")
		scriptContent := fmt.Sprintf(`#!/usr/bin/env clix
image: %s
entrypoint: /printpid
`, repo)

		if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("Failed to write script: %v", err)
		}

		var stdout, stderr bytes.Buffer
		stdin := strings.NewReader("")
		args := []string{"clix", scriptPath}

		err = run(stdin, &stdout, &stderr, args)
		if err != nil {
			t.Fatalf("run failed: %v, stderr: %s", err, stderr.String())
		}

		output := strings.TrimSpace(stdout.String())
		if output != "1" {
			t.Errorf("unexpected output: %q, expected '1'", output)
		}
	})
}
