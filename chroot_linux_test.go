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
	"net"
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

	t.Run("Network", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("failed to start listener: %v", err)
		}
		defer listener.Close()

		go func() {
			for {
				connection, err := listener.Accept()
				if err != nil {
					return
				}
				connection.Close()
			}
		}()

		hostAddress := listener.Addr().String()

		temporaryDirectory := t.TempDir()
		networkSourcePath := filepath.Join(temporaryDirectory, "main.go")
		sourceCode := `package main
import (
	"fmt"
	"net"
	"os"
	"time"
)
func main() {
	if len(os.Args) < 2 {
		fmt.Println("missing address")
		os.Exit(2)
	}
	connection, err := net.DialTimeout("tcp", os.Args[1], 1*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial error: %v\n", err)
		os.Exit(1)
	}
	connection.Close()
	fmt.Println("connected")
}
`
		if err := os.WriteFile(networkSourcePath, []byte(sourceCode), 0644); err != nil {
			t.Fatalf("failed to write net source: %v", err)
		}

		networkBinaryPath := filepath.Join(temporaryDirectory, "nettest")
		buildCommand := exec.Command("go", "build", "-o", networkBinaryPath, networkSourcePath)
		buildCommand.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
		if output, err := buildCommand.CombinedOutput(); err != nil {
			t.Fatalf("failed to build net binary: %v\noutput: %s", err, output)
		}

		binaryData, err := os.ReadFile(networkBinaryPath)
		if err != nil {
			t.Fatalf("failed to read compiled binary: %v", err)
		}

		var tarBuffer bytes.Buffer
		tarWriter := tar.NewWriter(&tarBuffer)
		header := &tar.Header{
			Name: "nettest",
			Mode: 0755,
			Size: int64(len(binaryData)),
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("failed to write tar header: %v", err)
		}
		if _, err := tarWriter.Write(binaryData); err != nil {
			t.Fatalf("failed to write tar body: %v", err)
		}
		if err := tarWriter.Close(); err != nil {
			t.Fatalf("failed to close tar writer: %v", err)
		}

		layer, err := tarball.LayerFromReader(&tarBuffer)
		if err != nil {
			t.Fatalf("failed to create layer: %v", err)
		}

		image, err := mutate.AppendLayers(empty.Image, layer)
		if err != nil {
			t.Fatalf("failed to append layer: %v", err)
		}

		testServer := httptest.NewServer(registry.New())
		defer testServer.Close()

		parsedURL, err := url.Parse(testServer.URL)
		if err != nil {
			t.Fatalf("failed to parse registry URL: %v", err)
		}

		repository := parsedURL.Host + "/net-test:latest"
		if err := crane.Push(image, repository); err != nil {
			t.Fatalf("failed to push image to local registry: %v", err)
		}

		t.Run("DefaultNetworkSucceeds", func(t *testing.T) {
			scriptPath := filepath.Join(temporaryDirectory, "test-script-default-net")
			scriptContent := fmt.Sprintf(`#!/usr/bin/env clix
image: %s
entrypoint: /nettest
`, repository)
			if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
				t.Fatalf("failed to write script: %v", err)
			}

			var stdout, stderr bytes.Buffer
			stdin := strings.NewReader("")
			args := []string{"clix", scriptPath, hostAddress}

			if err := run(stdin, &stdout, &stderr, args); err != nil {
				t.Fatalf("expected connection to succeed without network isolation: %v, stderr: %s", err, stderr.String())
			}
			if !strings.Contains(stdout.String(), "connected") {
				t.Errorf("expected 'connected' in stdout, got: %q", stdout.String())
			}
		})

		t.Run("NetworkNoneFails", func(t *testing.T) {
			scriptPath := filepath.Join(temporaryDirectory, "test-script-none-net")
			scriptContent := fmt.Sprintf(`#!/usr/bin/env clix
image: %s
entrypoint: /nettest
network: none
`, repository)
			if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
				t.Fatalf("failed to write script: %v", err)
			}

			var stdout, stderr bytes.Buffer
			stdin := strings.NewReader("")
			args := []string{"clix", scriptPath, hostAddress}

			err := run(stdin, &stdout, &stderr, args)
			if err == nil {
				t.Fatalf("expected connection to fail under network: none, but it succeeded; stdout: %s", stdout.String())
			}
			if !strings.Contains(stderr.String(), "dial error") {
				t.Errorf("expected 'dial error' in stderr, got: %q", stderr.String())
			}
		})
	})
}
