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

//go:build !linux

package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestChrootUnsupported(t *testing.T) {
	sandbox := &ChrootSandbox{}
	script := Config{
		Image: "test-image",
	}

	err := sandbox.Run(context.Background(), strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, script, []string{"echo", "hello"})
	if err == nil {
		t.Fatalf("expected error for chroot sandbox on unsupported platform, got nil")
	}
	expected := fmt.Sprintf("chroot sandbox is not supported on %s", runtime.GOOS)
	if !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}
