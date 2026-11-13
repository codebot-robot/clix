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
	"slices"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

func TestBuildSandboxEnv(t *testing.T) {
	const defaultPath = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	t.Setenv("CLIX_TEST_HOST", "from-host")

	tests := []struct {
		name      string
		imageEnv  []string
		scriptEnv []EnvVar
		want      []string
		dontWant  []string
	}{
		{
			name:     "image overrides base",
			imageEnv: []string{"PATH=/custom/bin"},
			want:     []string{"PATH=/custom/bin", "HOME=/root"},
			dontWant: []string{defaultPath},
		},
		{
			name:     "base kept when the image is silent",
			imageEnv: []string{"GOPATH=/go"},
			want:     []string{defaultPath, "HOME=/root", "GOPATH=/go"},
		},
		{
			name:      "script overrides image",
			imageEnv:  []string{"VAR=from_image"},
			scriptEnv: []EnvVar{{Name: "VAR", Value: "from_script"}},
			want:      []string{"VAR=from_script"},
			dontWant:  []string{"VAR=from_image"},
		},
		{
			name:     "host variable absent",
			dontWant: []string{"CLIX_TEST_HOST=from-host"},
		},
		{
			name:      "no duplicate names",
			imageEnv:  []string{"DUP=1", "DUP=2"},
			scriptEnv: []EnvVar{{Name: "DUP", Value: "3"}},
			want:      []string{"DUP=3"},
			dontWant:  []string{"DUP=1", "DUP=2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildSandboxEnv(tc.imageEnv, tc.scriptEnv)

			seen := make(map[string]bool)
			for _, entry := range got {
				k, _, _ := strings.Cut(entry, "=")
				if seen[k] {
					t.Errorf("duplicate variable %q in %v", k, got)
				}
				seen[k] = true
			}

			for _, want := range tc.want {
				if !slices.Contains(got, want) {
					t.Errorf("missing %q in %v", want, got)
				}
			}
			for _, dontWant := range tc.dontWant {
				if slices.Contains(got, dontWant) {
					t.Errorf("unexpected %q in %v", dontWant, got)
				}
			}
		})
	}
}

func TestImageEnv(t *testing.T) {
	img, err := mutate.Config(empty.Image, v1.Config{
		Env: []string{"A=1", "B=2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := imageEnv(img)
	if err != nil || !slices.Equal(env, []string{"A=1", "B=2"}) {
		t.Errorf("imageEnv with config: got %v, %v", env, err)
	}

	env, err = imageEnv(empty.Image)
	if err != nil || len(env) != 0 {
		t.Errorf("imageEnv without config: got %v, %v", env, err)
	}
}
