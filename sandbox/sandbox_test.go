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
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr"
)

type tarEntry struct {
	header tar.Header
	data   []byte
}

func createTar(t *testing.T, entries []tarEntry) io.Reader {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := e.header
		if len(e.data) > 0 && h.Size == 0 {
			h.Size = int64(len(e.data))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if len(e.data) > 0 {
			if _, err := tw.Write(e.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestUntar(t *testing.T) {
	tests := []struct {
		name      string
		entries   func(dest string) []tarEntry
		wantErr   bool
		wantFiles map[string]string
		wantLinks map[string]string
		wantModes map[string]os.FileMode
	}{
		{
			name: "plain files and directories",
			entries: func(dest string) []tarEntry {
				return []tarEntry{
					{header: tar.Header{Name: "dir/", Typeflag: tar.TypeDir, Mode: 0755}},
					{header: tar.Header{Name: "dir/private.txt", Typeflag: tar.TypeReg, Mode: 0600}, data: []byte("secret")},
					{header: tar.Header{Name: "dir/setuid.txt", Typeflag: tar.TypeReg, Mode: 04755}, data: []byte("exec")},
				}
			},
			wantFiles: map[string]string{
				"dir/private.txt": "secret",
				"dir/setuid.txt":  "exec",
			},
			wantModes: map[string]os.FileMode{
				"dir/private.txt": 0600,
				"dir/setuid.txt":  0755,
			},
		},
		{
			name: "symlink absolute and relative",
			entries: func(dest string) []tarEntry {
				return []tarEntry{
					{header: tar.Header{Name: "bin/sh", Typeflag: tar.TypeSymlink, Linkname: "/bin/busybox"}},
					{header: tar.Header{Name: "real_dir/", Typeflag: tar.TypeDir, Mode: 0755}},
					{header: tar.Header{Name: "sym_dir", Typeflag: tar.TypeSymlink, Linkname: "real_dir"}},
					{header: tar.Header{Name: "sym_dir/file.txt", Typeflag: tar.TypeReg, Mode: 0644}, data: []byte("content")},
				}
			},
			wantFiles: map[string]string{"real_dir/file.txt": "content"},
			wantLinks: map[string]string{"bin/sh": "/bin/busybox"},
		},
		{
			name: "root entry",
			entries: func(dest string) []tarEntry {
				return []tarEntry{{header: tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0755}}}
			},
		},
		{
			name: "entry dotdot",
			entries: func(dest string) []tarEntry {
				return []tarEntry{{header: tar.Header{Name: "../x", Typeflag: tar.TypeReg, Mode: 0644}, data: []byte("x")}}
			},
			wantErr: true,
		},
		{
			name: "entry leading slash",
			entries: func(dest string) []tarEntry {
				return []tarEntry{{header: tar.Header{Name: "/abs/x", Typeflag: tar.TypeReg, Mode: 0644}, data: []byte("x")}}
			},
			wantFiles: map[string]string{"abs/x": "x"},
		},
		{
			name: "entry prefix clash",
			entries: func(dest string) []tarEntry {
				return []tarEntry{{header: tar.Header{Name: "../" + filepath.Base(dest) + "-suffix/x", Typeflag: tar.TypeReg, Mode: 0644}, data: []byte("x")}}
			},
			wantErr: true,
		},
		{
			name: "symlink outside followed by file",
			entries: func(dest string) []tarEntry {
				return []tarEntry{
					{header: tar.Header{Name: "out", Typeflag: tar.TypeSymlink, Linkname: "../outside"}},
					{header: tar.Header{Name: "out/file.txt", Typeflag: tar.TypeReg, Mode: 0644}, data: []byte("x")},
				}
			},
			wantErr: true,
		},
		{
			name: "symlink chain followed by regular file",
			entries: func(dest string) []tarEntry {
				return []tarEntry{
					{header: tar.Header{Name: "a/b/", Typeflag: tar.TypeDir, Mode: 0755}},
					{header: tar.Header{Name: "a/b/s", Typeflag: tar.TypeSymlink, Linkname: ".."}},
					{header: tar.Header{Name: "e", Typeflag: tar.TypeSymlink, Linkname: "a/b/s/../../outside"}},
					{header: tar.Header{Name: "e", Typeflag: tar.TypeReg, Mode: 0644}, data: []byte("regular")},
				}
			},
			wantFiles: map[string]string{"e": "regular"},
		},
		{
			name: "repeated regular file",
			entries: func(dest string) []tarEntry {
				return []tarEntry{
					{header: tar.Header{Name: "file.txt", Typeflag: tar.TypeReg, Mode: 0644}, data: []byte("first")},
					{header: tar.Header{Name: "file.txt", Typeflag: tar.TypeReg, Mode: 0644}, data: []byte("second")},
				}
			},
			wantFiles: map[string]string{"file.txt": "second"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "dest")
			if err := os.Mkdir(dest, 0755); err != nil {
				t.Fatal(err)
			}

			err := untar(createTar(t, tc.entries(dest)), dest)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for path, want := range tc.wantFiles {
				got, err := os.ReadFile(filepath.Join(dest, path))
				if err != nil || string(got) != want {
					t.Fatalf("ReadFile(%s) = %q, %v; want %q", path, got, err, want)
				}
			}
			for path, want := range tc.wantLinks {
				got, err := os.Readlink(filepath.Join(dest, path))
				if err != nil || got != want {
					t.Fatalf("Readlink(%s) = %q, %v; want %q", path, got, err, want)
				}
			}
			for path, want := range tc.wantModes {
				fi, err := os.Stat(filepath.Join(dest, path))
				if err != nil || fi.Mode().Perm() != want {
					t.Fatalf("Mode(%s) = %v, %v; want %v", path, fi.Mode().Perm(), err, want)
				}
			}

			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatalf("ReadDir parent: %v", err)
			}
			if len(entries) != 1 || entries[0].Name() != filepath.Base(dest) {
				t.Fatalf("files created outside destination directory: %v", entries)
			}
		})
	}
}

func TestBackendArgumentsIdentical(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd failed: %v", err)
	}

	script := Config{
		Image:      "my-image:v1",
		Entrypoint: "/bin/mytool",
		Mounts: []Mount{
			{HostPath: "/host/ro", SandboxPath: "/sandbox/ro", ReadOnly: true, WritableFallback: true},
			{HostPath: "/host/rw", SandboxPath: "/sandbox/rw", ReadOnly: false},
		},
		Env: []EnvVar{
			{Name: "VAR_A", Value: "VAL_A"},
		},
	}
	args := []string{"run", "--flag"}

	t.Run("Docker", func(t *testing.T) {
		cmdArgs, err := buildDockerArgs(script, args, false)
		if err != nil {
			t.Fatalf("buildDockerArgs failed: %v", err)
		}
		expected := []string{
			"run", "--rm", "-i",
			"-v", "/host/ro:/sandbox/ro:ro",
			"-v", "/host/rw:/sandbox/rw",
			"-e", "VAR_A=VAL_A",
			"-w", cwd,
			"--entrypoint", "/bin/mytool",
			"my-image:v1",
			"run", "--flag",
		}
		if !slices.Equal(cmdArgs, expected) {
			t.Errorf("got %v, want %v", cmdArgs, expected)
		}
	})

	t.Run("AppleContainer", func(t *testing.T) {
		cmdArgs, err := buildAppleContainerArgs(script, args, false)
		if err != nil {
			t.Fatalf("buildAppleContainerArgs failed: %v", err)
		}
		expected := []string{
			"run", "--rm",
			"--mount", "type=bind,source=/host/ro,target=/sandbox/ro,readonly",
			"--mount", "type=bind,source=/host/rw,target=/sandbox/rw",
			"-e", "VAR_A=VAL_A",
			"-w", cwd,
			"--entrypoint", "/bin/mytool",
			"my-image:v1",
			"run", "--flag",
		}
		if !slices.Equal(cmdArgs, expected) {
			t.Errorf("got %v, want %v", cmdArgs, expected)
		}
	})

	t.Run("Proot", func(t *testing.T) {
		oldPrepareRootFS := prepareRootFSFunction
		defer func() { prepareRootFSFunction = oldPrepareRootFS }()
		prepareRootFSFunction = func(ctx context.Context, imageRef string) (*preparedRootFS, error) {
			return &preparedRootFS{
				dir:     "/mock/rootfs",
				cleanup: func() {},
			}, nil
		}

		var capturedArgs []string
		oldExecCommand := execCommand
		defer func() { execCommand = oldExecCommand }()
		execCommand = func(name string, args ...string) *exec.Cmd {
			capturedArgs = args
			return fakeExecCommand(name, args...)
		}

		sandbox := &prootSandbox{}
		var stdout, stderr bytes.Buffer
		err := sandbox.Run(t.Context(), strings.NewReader(""), &stdout, &stderr, script, args)
		if err != nil {
			t.Fatalf("sandbox.Run failed: %v", err)
		}
		expected := []string{
			"-r", "/mock/rootfs",
			"-b", "/host/ro:/sandbox/ro",
			"-b", "/host/rw:/sandbox/rw",
			"/bin/mytool",
			"run", "--flag",
		}
		if !slices.Equal(capturedArgs, expected) {
			t.Errorf("got %v, want %v", capturedArgs, expected)
		}
	})
}

type recordingLogSink struct {
	messages []string
}

func (s *recordingLogSink) Init(info logr.RuntimeInfo)                   {}
func (s *recordingLogSink) Enabled(level int) bool                       { return true }
func (s *recordingLogSink) WithValues(keysAndValues ...any) logr.LogSink { return s }
func (s *recordingLogSink) WithName(name string) logr.LogSink            { return s }

func (s *recordingLogSink) Info(level int, message string, keysAndValues ...any) {
	s.messages = append(s.messages, message)
}

func (s *recordingLogSink) Error(err error, message string, keysAndValues ...any) {
	s.messages = append(s.messages, message)
}

func TestLibraryCallerLoggerReceivesMessages(t *testing.T) {
	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand

	sink := &recordingLogSink{}
	logger := logr.New(sink)
	ctx := logr.NewContext(t.Context(), logger)

	sandbox := &dockerSandbox{}
	script := Config{Image: "alpine"}
	var stdout, stderr bytes.Buffer
	err := sandbox.Run(ctx, strings.NewReader(""), &stdout, &stderr, script, []string{"echo", "hello"})
	if err != nil {
		t.Fatalf("sandbox.Run failed: %v", err)
	}

	expectedPrefixes := []string{
		"DockerSandbox: preparing args",
		"DockerSandbox: running docker",
	}
	for _, prefix := range expectedPrefixes {
		found := false
		for _, message := range sink.messages {
			if strings.HasPrefix(message, prefix) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected message starting with %q in recorded messages: %v", prefix, sink.messages)
		}
	}
}

func TestNoLoggerNoOutputToStderr(t *testing.T) {
	oldExecCommand := execCommand
	defer func() { execCommand = oldExecCommand }()
	execCommand = fakeExecCommand

	t.Setenv("CLIX_LOG_VERBOSITY", "2")

	oldStderr := os.Stderr
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stderr = pipeWriter

	sandbox := &dockerSandbox{}
	script := Config{Image: "alpine"}
	var stdout, stderr bytes.Buffer
	runErr := sandbox.Run(t.Context(), strings.NewReader(""), &stdout, &stderr, script, []string{"echo", "hello"})

	if err := pipeWriter.Close(); err != nil {
		t.Errorf("failed to close pipe writer: %v", err)
	}
	os.Stderr = oldStderr

	var capturedStderr bytes.Buffer
	if _, err := io.Copy(&capturedStderr, pipeReader); err != nil {
		t.Fatalf("failed to read from pipe: %v", err)
	}
	if err := pipeReader.Close(); err != nil {
		t.Errorf("failed to close pipe reader: %v", err)
	}

	if runErr != nil {
		t.Fatalf("sandbox.Run failed: %v", runErr)
	}

	if capturedStderr.Len() > 0 {
		t.Errorf("expected nothing written to process stderr, got %q", capturedStderr.String())
	}
}

func TestUnsupportedBackendsRejectNetworkIsolation(t *testing.T) {
	testCases := []struct {
		backendName     string
		expectedMessage string
	}{
		{
			backendName:     "apple-container",
			expectedMessage: "network isolation is not supported by the apple-container sandbox",
		},
		{
			backendName:     "chroot",
			expectedMessage: "network isolation is not supported by the chroot sandbox",
		},
		{
			backendName:     "proot",
			expectedMessage: "network isolation is not supported by the proot sandbox",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.backendName, func(t *testing.T) {
			backend, err := New(testCase.backendName)
			if err != nil {
				t.Fatalf("New failed: %v", err)
			}
			config := Config{
				Image:   "alpine",
				Network: "none",
			}
			var stdout, stderr bytes.Buffer
			err = backend.Run(t.Context(), strings.NewReader(""), &stdout, &stderr, config, []string{"echo", "hi"})
			if err == nil {
				t.Fatalf("expected error for backend %s, got nil", testCase.backendName)
			}
			if !strings.Contains(err.Error(), testCase.expectedMessage) {
				t.Errorf("got error %q, want %q", err.Error(), testCase.expectedMessage)
			}
		})
	}
}

func TestMountValidation(t *testing.T) {
	backends := []struct {
		name             string
		delimiter        string
		allowedCharacter string
	}{
		{name: "docker", delimiter: ":", allowedCharacter: ","},
		{name: "apple-container", delimiter: ",", allowedCharacter: ":"},
		{name: "proot", delimiter: ":", allowedCharacter: ","},
	}

	for _, targetBackend := range backends {
		t.Run(targetBackend.name, func(t *testing.T) {
			backend, err := New(targetBackend.name)
			if err != nil {
				t.Fatalf("New(%q) failed: %v", targetBackend.name, err)
			}

			invalidCases := []struct {
				name            string
				mount           Mount
				expectedMessage string
			}{
				{
					name:            "empty host path",
					mount:           Mount{HostPath: "", SandboxPath: "/sandbox"},
					expectedMessage: `mount "/sandbox": host path is empty`,
				},
				{
					name:            "relative host path",
					mount:           Mount{HostPath: "relative/path", SandboxPath: "/sandbox"},
					expectedMessage: `mount "relative/path": host path is not absolute`,
				},
				{
					name:            "empty sandbox path",
					mount:           Mount{HostPath: "/host", SandboxPath: ""},
					expectedMessage: `mount "/host": sandbox path is empty`,
				},
				{
					name:            "relative sandbox path",
					mount:           Mount{HostPath: "/host", SandboxPath: "relative/sandbox"},
					expectedMessage: `mount "/host": sandbox path "relative/sandbox" is not absolute`,
				},
				{
					name:            "delimiter in host path",
					mount:           Mount{HostPath: "/host" + targetBackend.delimiter + "path", SandboxPath: "/sandbox"},
					expectedMessage: fmt.Sprintf(`mount "/host%spath": host path contains '%s'`, targetBackend.delimiter, targetBackend.delimiter),
				},
				{
					name:            "delimiter in sandbox path",
					mount:           Mount{HostPath: "/host", SandboxPath: "/sandbox" + targetBackend.delimiter + "path"},
					expectedMessage: fmt.Sprintf(`mount "/host": sandbox path "/sandbox%spath" contains '%s'`, targetBackend.delimiter, targetBackend.delimiter),
				},
			}

			for _, testCase := range invalidCases {
				t.Run(testCase.name, func(t *testing.T) {
					oldExecCommand := execCommand
					defer func() { execCommand = oldExecCommand }()
					execCommand = func(name string, args ...string) *exec.Cmd {
						t.Fatalf("execCommand was called for invalid mount: %s %v", name, args)
						return nil
					}

					oldPrepareRootFS := prepareRootFSFunction
					defer func() { prepareRootFSFunction = oldPrepareRootFS }()
					prepareRootFSFunction = func(ctx context.Context, imageRef string) (*preparedRootFS, error) {
						t.Fatalf("prepareRootFS was called for invalid mount")
						return nil, nil
					}

					config := Config{
						Image:  "alpine",
						Mounts: []Mount{testCase.mount},
					}
					var stdout, stderr bytes.Buffer
					err := backend.Run(t.Context(), strings.NewReader(""), &stdout, &stderr, config, []string{"echo", "hi"})
					if err == nil {
						t.Fatalf("expected error for invalid mount %+v, got nil", testCase.mount)
					}
					if !strings.Contains(err.Error(), testCase.expectedMessage) {
						t.Errorf("got error %q, want it to contain %q", err.Error(), testCase.expectedMessage)
					}
				})
			}

			t.Run("character allowed on other backends is accepted", func(t *testing.T) {
				oldExecCommand := execCommand
				defer func() { execCommand = oldExecCommand }()
				execCommand = fakeExecCommand

				oldPrepareRootFS := prepareRootFSFunction
				defer func() { prepareRootFSFunction = oldPrepareRootFS }()
				prepareRootFSFunction = func(ctx context.Context, imageRef string) (*preparedRootFS, error) {
					return &preparedRootFS{dir: "/mock", cleanup: func() {}}, nil
				}

				config := Config{
					Image: "alpine",
					Mounts: []Mount{
						{HostPath: "/host" + targetBackend.allowedCharacter + "ok", SandboxPath: "/sandbox" + targetBackend.allowedCharacter + "ok"},
					},
				}
				var stdout, stderr bytes.Buffer
				err := backend.Run(t.Context(), strings.NewReader(""), &stdout, &stderr, config, []string{"echo", "hi"})
				if err != nil {
					t.Fatalf("expected valid character %q to be accepted on %s, got error: %v", targetBackend.allowedCharacter, targetBackend.name, err)
				}
			})
		})
	}
}

func TestWithExecutableBackends(t *testing.T) {
	oldPrepareRootFS := prepareRootFSFunction
	defer func() { prepareRootFSFunction = oldPrepareRootFS }()
	prepareRootFSFunction = func(ctx context.Context, imageRef string) (*preparedRootFS, error) {
		return &preparedRootFS{dir: t.TempDir(), cleanup: func() {}}, nil
	}

	runBackend := func(t *testing.T, backend Sandbox) string {
		t.Helper()
		var executedCommand string
		oldExecCommand := execCommand
		defer func() { execCommand = oldExecCommand }()
		execCommand = func(name string, args ...string) *exec.Cmd {
			executedCommand = name
			return fakeExecCommand(name, args...)
		}
		var stdout, stderr bytes.Buffer
		if err := backend.Run(t.Context(), strings.NewReader(""), &stdout, &stderr, Config{Image: "alpine"}, []string{"echo"}); err != nil {
			t.Fatalf("backend.Run failed: %v", err)
		}
		return executedCommand
	}

	for _, backendName := range []string{"", "docker", "apple-container", "proot"} {
		testName := backendName
		if testName == "" {
			testName = "default"
		}
		t.Run(testName, func(t *testing.T) {
			targetBackend := backendName
			if targetBackend == "" {
				targetBackend = "docker"
			}
			customPath := "/custom/bin/" + targetBackend
			backend, err := New(backendName, WithExecutable(targetBackend, customPath))
			if err != nil {
				t.Fatalf("New failed: %v", err)
			}
			if executed := runBackend(t, backend); executed != customPath {
				t.Errorf("got executed command %q, want %q", executed, customPath)
			}
		})
	}

	t.Run("OptionForDifferentBackendIgnored", func(t *testing.T) {
		backend, err := New("docker", WithExecutable("proot", "/custom/bin/proot"))
		if err != nil {
			t.Fatalf("New failed: %v", err)
		}
		if executed := runBackend(t, backend); executed != "docker" {
			t.Errorf("expected default docker executable, got %q", executed)
		}
	})

	t.Run("MultipleBackendOptionsProvided", func(t *testing.T) {
		backend, err := New("proot",
			WithExecutable("docker", "/custom/bin/docker"),
			WithExecutable("proot", "/custom/bin/proot"),
		)
		if err != nil {
			t.Fatalf("New failed: %v", err)
		}
		if executed := runBackend(t, backend); executed != "/custom/bin/proot" {
			t.Errorf("expected proot executable from multiple options, got %q", executed)
		}
	})

	t.Run("ChrootIgnoresExecutableOption", func(t *testing.T) {
		backend, err := New("chroot", WithExecutable("docker", "/custom/bin/docker"))
		if err != nil || backend == nil {
			t.Fatalf("expected chroot to ignore executable option, got err: %v", err)
		}
	})
}
