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
	"context"
	"fmt"
	"io"
	"runtime"
)

type chrootSandbox struct{}

var checkUserNamespacesFunction = func(ctx context.Context) error {
	return fmt.Errorf("chroot sandbox is not supported on %s", runtime.GOOS)
}

func (s *chrootSandbox) Usable(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("command cancelled: %w", err)
	}
	return checkUserNamespacesFunction(ctx)
}

func (s *chrootSandbox) String() string {
	return "chroot"
}

func (s *chrootSandbox) Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, script Config, args []string) error {
	if script.Network != "" && script.Network != "none" {
		return fmt.Errorf("network mode %q is not supported by the chroot sandbox", script.Network)
	}
	return fmt.Errorf("chroot sandbox is not supported on %s", runtime.GOOS)
}
