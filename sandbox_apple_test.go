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
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestBuildImageAppleContainer(t *testing.T) {
	oldExec := execCommand
	defer func() { execCommand = oldExec }()

	execCommand = fakeExecCommand

	os.Setenv("CLIX_SANDBOX", "apple-container")
	defer os.Unsetenv("CLIX_SANDBOX")

	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader("")

	build := &BuildConfig{
		Git: "https://github.com/example/repo",
	}

	_, err := buildImage(stdin, &stdout, &stderr, build, "test-script.yaml")
	if err != nil {
		t.Fatalf("buildImage failed: %v", err)
	}

	outStr := stderr.String()
	if !strings.Contains(outStr, "Cloning") {
		t.Errorf("Expected cloning message, got: %s", outStr)
	}
	if !strings.Contains(outStr, "Building image") {
		t.Errorf("Expected building message, got: %s", outStr)
	}
}

func TestGetAppleContainerImageSHA(t *testing.T) {
	// Mock execCommand
	oldExec := execCommand
	defer func() { execCommand = oldExec }()

	execCommand = fakeExecCommand

	sha, err := getAppleContainerImageSHA("test-image")
	if err != nil {
		t.Fatalf("getAppleContainerImageSHA failed: %v", err)
	}

	expectedSHA := "abcdef123456"
	if sha != expectedSHA {
		t.Errorf("Expected SHA %s, got %s", expectedSHA, sha)
	}
}
