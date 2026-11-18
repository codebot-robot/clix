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

//go:build unix

package sandbox

import (
	"errors"
	"io"
	"os/exec"
	"syscall"
)

// setupProcessGroup places cmd into its own process group if stdin is not a terminal.
// A process outside the terminal's foreground group is stopped when it reads the
// terminal and does not receive Ctrl-C (delivered only to the foreground group), so
// interactive runs keep the caller's group. The trade-off is that on cancellation
// only the direct process is killed rather than an entire process group.
func setupProcessGroup(cmd *exec.Cmd, stdin io.Reader) {
	if isTerminal(stdin) {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	} else {
		cmd.SysProcAttr.Setpgid = true
	}
}

func hasProcessGroup(cmd *exec.Cmd) bool {
	return cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid
}

func killProcessGroup(processID int) error {
	if processID <= 0 {
		return nil
	}
	err := syscall.Kill(-processID, syscall.SIGKILL)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
