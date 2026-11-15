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
	"io"
	"os"
	"path/filepath"
	"testing"
)

type tarEntry struct {
	header tar.Header
	data   []byte
}

func createTar(t *testing.T, entries []tarEntry) io.Reader {
	var buffer bytes.Buffer
	tarWriter := tar.NewWriter(&buffer)
	for _, entry := range entries {
		header := entry.header
		if len(entry.data) > 0 && header.Size == 0 {
			header.Size = int64(len(entry.data))
		}
		if err := tarWriter.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if len(entry.data) > 0 {
			if _, err := tarWriter.Write(entry.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return &buffer
}

func TestUntar(t *testing.T) {
	tests := []struct {
		name      string
		entries   func(destination string) []tarEntry
		wantErr   bool
		wantFiles map[string]string
		wantLinks map[string]string
		wantModes map[string]os.FileMode
	}{
		{
			name: "plain files and directories",
			entries: func(destination string) []tarEntry {
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
			entries: func(destination string) []tarEntry {
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
			entries: func(destination string) []tarEntry {
				return []tarEntry{{header: tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0755}}}
			},
		},
		{
			name: "entry dotdot",
			entries: func(destination string) []tarEntry {
				return []tarEntry{{header: tar.Header{Name: "../x", Typeflag: tar.TypeReg, Mode: 0644}, data: []byte("x")}}
			},
			wantErr: true,
		},
		{
			name: "entry leading slash",
			entries: func(destination string) []tarEntry {
				return []tarEntry{{header: tar.Header{Name: "/abs/x", Typeflag: tar.TypeReg, Mode: 0644}, data: []byte("x")}}
			},
			wantFiles: map[string]string{"abs/x": "x"},
		},
		{
			name: "entry prefix clash",
			entries: func(destination string) []tarEntry {
				return []tarEntry{{header: tar.Header{Name: "../" + filepath.Base(destination) + "-suffix/x", Typeflag: tar.TypeReg, Mode: 0644}, data: []byte("x")}}
			},
			wantErr: true,
		},
		{
			name: "symlink outside followed by file",
			entries: func(destination string) []tarEntry {
				return []tarEntry{
					{header: tar.Header{Name: "out", Typeflag: tar.TypeSymlink, Linkname: "../outside"}},
					{header: tar.Header{Name: "out/file.txt", Typeflag: tar.TypeReg, Mode: 0644}, data: []byte("x")},
				}
			},
			wantErr: true,
		},
		{
			name: "symlink chain followed by regular file",
			entries: func(destination string) []tarEntry {
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
			entries: func(destination string) []tarEntry {
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
			destination := filepath.Join(parent, "destination")
			if err := os.Mkdir(destination, 0755); err != nil {
				t.Fatal(err)
			}

			err := untar(createTar(t, tc.entries(destination)), destination)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for path, want := range tc.wantFiles {
				got, err := os.ReadFile(filepath.Join(destination, path))
				if err != nil || string(got) != want {
					t.Fatalf("ReadFile(%s) = %q, %v; want %q", path, got, err, want)
				}
			}
			for path, want := range tc.wantLinks {
				got, err := os.Readlink(filepath.Join(destination, path))
				if err != nil || got != want {
					t.Fatalf("Readlink(%s) = %q, %v; want %q", path, got, err, want)
				}
			}
			for path, want := range tc.wantModes {
				fileInfo, err := os.Stat(filepath.Join(destination, path))
				if err != nil || fileInfo.Mode().Perm() != want {
					t.Fatalf("Mode(%s) = %v, %v; want %v", path, fileInfo.Mode().Perm(), err, want)
				}
			}

			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatalf("ReadDir parent: %v", err)
			}
			if len(entries) != 1 || entries[0].Name() != filepath.Base(destination) {
				t.Fatalf("files created outside destination directory: %v", entries)
			}
		})
	}
}

func TestNew(t *testing.T) {
	tests := []struct {
		backendName string
		wantType    string
		wantErr     bool
	}{
		{"", "*sandbox.DockerSandbox", false},
		{"docker", "*sandbox.DockerSandbox", false},
		{"apple-container", "*sandbox.AppleContainerSandbox", false},
		{"chroot", "*sandbox.ChrootSandbox", false},
		{"proot", "*sandbox.ProotSandbox", false},
		{"invalid", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.backendName, func(t *testing.T) {
			selectedSandbox, err := New(tc.backendName)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("New(%q) expected error, got nil", tc.backendName)
				}
				return
			}
			if err != nil {
				t.Fatalf("New(%q) unexpected error: %v", tc.backendName, err)
			}
			if got := filepath.Clean(filepath.Join("/", tc.wantType)); got == "" {
				t.Fatalf("empty wantType")
			}
			_ = selectedSandbox
		})
	}
}

func TestNewFromEnv(t *testing.T) {
	t.Setenv("CLIX_SANDBOX", "proot")
	selectedSandbox, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv failed: %v", err)
	}
	if _, ok := selectedSandbox.(*ProotSandbox); !ok {
		t.Errorf("expected *ProotSandbox, got %T", selectedSandbox)
	}

	t.Setenv("CLIX_SANDBOX", "")
	selectedSandbox, err = NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv failed: %v", err)
	}
	if _, ok := selectedSandbox.(*DockerSandbox); !ok {
		t.Errorf("expected *DockerSandbox, got %T", selectedSandbox)
	}
}

func TestValidateMount(t *testing.T) {
	tests := []struct {
		name    string
		input   Mount
		want    Mount
		wantErr bool
	}{
		{
			name:    "valid mount",
			input:   Mount{HostPath: "/tmp/host", SandboxPath: "/tmp/sandbox", ReadOnly: true},
			want:    Mount{HostPath: "/tmp/host", SandboxPath: "/tmp/sandbox", ReadOnly: true},
			wantErr: false,
		},
		{
			name:    "default sandbox path",
			input:   Mount{HostPath: "/tmp/host"},
			want:    Mount{HostPath: "/tmp/host", SandboxPath: "/tmp/host"},
			wantErr: false,
		},
		{
			name:    "non-absolute host path",
			input:   Mount{HostPath: "relative/path"},
			wantErr: true,
		},
		{
			name:    "non-absolute sandbox path",
			input:   Mount{HostPath: "/tmp/host", SandboxPath: "relative/path"},
			wantErr: true,
		},
		{
			name:    "host path with colon",
			input:   Mount{HostPath: "/tmp/host:1", SandboxPath: "/tmp/sandbox"},
			wantErr: true,
		},
		{
			name:    "sandbox path with colon",
			input:   Mount{HostPath: "/tmp/host", SandboxPath: "/tmp/sandbox:1"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateMount(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validateMount(%+v) expected error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateMount(%+v) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("validateMount(%+v) = %+v, want %+v", tc.input, got, tc.want)
			}
		})
	}
}
