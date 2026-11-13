# Developer & Agent Guidelines

This document describes the project outline and architecture (section 1), how to run the checks locally (section 2), PR scope (section 3), naming conventions (section 4), error handling (section 5), commit messages and PR descriptions (section 6), and coding guidelines (section 7).

---

## 1. Project Outline

`clix` is a tool for securely running CLI tools using images, running them in containers.

### Goals

* **Secure Execution**: Run tools in isolated environments (containers, sandboxes).
* **Easy Installation**: Managing tools via "scripts" that define how to run them.
* **Cross-Platform**: Support for Mac, Windows, Linux.
* **Selective Sandboxing**: Fine-grained control over what resources a tool can access (e.g., credentials).

### Script Format

Scripts are executable files with a shebang pointing to `clix`.

Example:

```yaml
#!/usr/bin/env clix

# image: future use
go:
  run: sigs.k8s.io/kustomize/kustomize/v5
  version: v5.8.0
```

`clix` will parse this and invoke:
`go run sigs.k8s.io/kustomize/kustomize/v5@v5.8.0 <args>`

---

## 2. Running Checks Locally

### Prerequisites

To run the checks locally, the following tools must be available in your execution environment:

1. **Go Toolchain** (the Go version in `go.mod`, or run with `GOTOOLCHAIN=auto` so Go resolves matching versions automatically).
2. **Docker Client CLI** and a running **Docker daemon** (accessible via `DOCKER_HOST` or the local Docker socket) for the end-to-end tests.

### Presubmit Scripts

The repository provides four presubmit scripts under `dev/ci/presubmits/`:

- **`dev/ci/presubmits/ap-lint`**: Runs linting and static analysis across the repository, including `go vet`, `govulncheck`, and analyzers enforcing error handling rules.
- **`dev/ci/presubmits/ap-test`**: Runs the unit and integration tests (`go test`).
- **`dev/ci/presubmits/ap-e2e`**: Runs the end-to-end tests (`tests/e2e/e2e_test.go`). Requires a working Docker daemon.
- **`dev/ci/presubmits/ap-verify-generate`**: Verifies that generated files (presubmit scripts and GitHub Actions workflows) are up-to-date with `.ap/` configuration. If changes are detected, run `go run github.com/gke-labs/gke-labs-infra/ap@latest generate //...` and commit the result.

Execute them with `GOTOOLCHAIN=auto`:

```bash
GOTOOLCHAIN=auto ./dev/ci/presubmits/ap-lint
GOTOOLCHAIN=auto ./dev/ci/presubmits/ap-test
GOTOOLCHAIN=auto ./dev/ci/presubmits/ap-e2e
GOTOOLCHAIN=auto ./dev/ci/presubmits/ap-verify-generate
```

---

## 3. Scope

- **One idea, one issue, one PR.** If a PR can be split into two ideas, split it; problems found during review that are outside the PR's idea become new issues.
- **A good PR changes at most 200 lines, including tests.** The time to review and understand a PR grows roughly with the square of the lines in the diff, so smaller PRs are reviewed faster and with fewer defects. If the one idea needs more than 200 lines, discuss it on the issue before pushing and propose where to cut or how to order the parts.

---

## 4. Naming

**Do not abbreviate identifiers.** Write the full word: `originalHostPath` not `origHostPath`, `string` not `str`, `buffer` not `buf`, `header` not `hdr`, `destination` not `dest`, `source` not `src`. Partial abbreviations are harder to read than the full word. An abbreviation saves the writer a few keystrokes and costs every reader a lookup, and review time is the bottleneck.

Exceptions, all of them conventional Go and nothing else:
- `i`, `j`, `k` as integer loop indices;
- a one- or two-letter method receiver (for example, `s *DockerSandbox`);
- `ctx`, `err`, `ok`, `cmd`, `dir`, `wg`, and `mu`, which are idioms across the Go ecosystem and this codebase (`context` and `error` are also package and type names);
- a protocol, format, or on-disk field name that is itself the abbreviation (such as `fd`, `sha256`, `uid`, `gid`, `stdin`, `stdout`, `stderr`).

Names must also match signatures and behavior: use one name per concept consistently across the codebase.

---

## 5. Error Handling

**Never discard an error silently.** Silently discarding errors hides corrupted state, data loss, and unexpected failures. Every error must be handled in one of three ways:

1. **Return to the caller** when the current function cannot safely complete or recover:
   ```go
   if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
       return fmt.Errorf("failed to create directory: %w", err)
   }
   ```

2. **Report or log with technical context** when the operation is best-effort or part of cleanup where failure should not abort the caller (e.g., printing a diagnostic warning to stderr):
   ```go
   if err := f.Close(); err != nil {
       fmt.Fprintf(os.Stderr, "Warning: failed to close file %s: %v\n", path, err)
   }
   ```

3. **Handle explicitly** when a specific error condition is expected and benign:
   ```go
   if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
       return fmt.Errorf("failed to remove %s: %w", path, err)
   }
   ```

### Enforcement

`dev/ci/presubmits/ap-lint` enforces error handling via static analyzers. Discarded or unhandled errors in new or modified code will fail the lint presubmit.

---

## 6. Commit Messages and PR Descriptions

**The code and its commit history must stand on their own.** PR descriptions do not become part of the repository; commit messages do. Anything a future reader needs to understand a change belongs in the commit messages, and the PR description carries only the issue reference.

- **Subject line:** `area: imperative summary`, about 72 characters at most, where `area` is the component or concept touched (for example `mounts`, `docker`, `sandbox`, `docs`, `script`).
- **Body: explain why, not just what.** Include whatever the diff alone doesn't make obvious:
  - **Bug fixes:** the symptom, the root cause, why the change fixes it, and the test that reproduces it.
  - **Design changes:** the approach taken, and the alternatives considered and why they were rejected.
  - **Review answers:** if a reviewer had to ask, the answer belongs in the message.
- **One commit per PR.** Keep each PR to a single commit. Amend the commit across review rounds (`git commit --amend`) rather than creating extra fixup or merge commits.
- **Keep messages true to the final code.** When review changes the approach, make sure the commit message describes what the code does now, not the history of attempts. Never leave a message describing code that was later removed or revised.
- **Reference issues in the commit body:** Include `Fixes #N` for the issue being closed. Put this in the commit body, not on the subject line. Every PR closes exactly one issue (do not use `Part of #N`).
- **Keep the PR description to bookkeeping only:** The PR description is `Fixes #N` only. Do not summarize or sell the change, do not paste the commit message, and do not maintain a changelog of review revisions in the description. The code and the commit stand on their own; a reviewer who needs more should find it in the commit message.

---

## 7. Coding Guidelines

- **Avoid "Train of Thought" Comments:** When writing code, do not include comments that describe the process of discovery (e.g., "Based on search results," or "According to the issue..."). Focus on explaining the final code's behavior or why it was implemented that way, rather than the research process.
