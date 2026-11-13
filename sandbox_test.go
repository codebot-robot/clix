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
