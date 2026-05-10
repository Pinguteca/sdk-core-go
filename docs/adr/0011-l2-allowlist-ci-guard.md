# ADR 0011: L2 dependency allow-list CI guard for Go

- **Status:** Accepted
- **Date:** 2026-05-10
- **Deciders:** SDK team
- **Affects:** root `go.mod`; `mise.toml` lint chain; new `tools/check-l2-deps` directory.
- **Implements:** RFC 0003 for Go.

## Context

RFC 0002 fixed the per-language allow list for Layer 2 dependencies
(stdlib + `golang.org/x/*` + Connect runtime exemption). RFC 0003
required every Primary-tier SDK to ship a CI guard that fails on
disallowed direct deps. This ADR pins the Go-specific implementation.

Two questions had to be resolved:

1. **How to parse `go.mod` without re-introducing a dep we are
   trying to constrain.** Importing `golang.org/x/mod/modfile`
   would be allow-listed (it is `golang.org/x/`), but every dep in
   the tool's own module bloats the workspace and a separate
   `go.mod` for the tool adds publication noise.
2. **Where the guard runs.** A standalone CI step is cheap to add
   but doubles the contributor's mental model; folding into the
   existing `mise run lint` keeps one chain.

## Decision

1. **Ship `tools/check-l2-deps/main.go` as a small Go program in the
   repo's root module.** Implementation uses `go mod edit -json` via
   `os/exec` so it has zero extra deps. The JSON output is parsed
   with `encoding/json`, direct-dep filter is `Indirect == false`.

2. **Allow list lives at `tools/check-l2-deps/allowlist.txt`.** One
   entry per line, hash-prefixed comments allowed, empty lines
   ignored. Two match shapes:
   - Exact: `connectrpc.com/connect`
   - Prefix glob: `golang.org/x/*` (suffix `/*` matches any path
     starting with the prefix).

3. **Initial allow-list contents:**
   ```
   # Connect runtime exemption (RFC 0002).
   connectrpc.com/connect
   google.golang.org/protobuf
   google.golang.org/genproto/googleapis/*

   # Stdlib-adjacent first-party (RFC 0002).
   golang.org/x/*
   ```

4. **Wire into `mise run lint` as `lint:l2-deps`.** The aggregate
   `lint` task depends on it; CI's existing build-go workflow runs
   `mise run lint` so no new workflow file is needed.

5. **Scope: root `go.mod` only.** Layer 3 sub-modules are explicitly
   allowed to depend on whatever they wrap (RFC 0002). Re-running
   the guard on them would defeat the layer split.

6. **Failure output names both remediation paths** (add to allow
   list and update RFC 0002, or move the package to a Layer 3
   companion sub-module). Same wording as RFC 0003 prescribes.

7. **No `go run` at lint time.** The tool is invoked via `go run
   ./tools/check-l2-deps` from the mise task. `go run` builds and
   executes in one step; no compiled artifact is committed.

## Consequences

### Positive

- Zero new third-party deps, including in the tool itself.
- Drift detection is automatic: a contributor adding a disallowed
  dep sees the failure on their first local `mise run lint`.
- The allow-list file is plain text, easy to grep and diff in code
  review.

### Negative

- The allow-list file duplicates information that RFC 0002 already
  encodes (this is an RFC 0003 drawback, inherited).
- The tool shells out to `go` rather than using `golang.org/x/mod`.
  If `go mod edit -json` output ever changes shape, the tool needs
  updating. Acceptable: `-json` output is a stability surface the
  Go team has held for several releases.
- One more `go run` step in `mise run lint`. Adds about 200 ms to
  local lint time; CI cost is negligible.

### Neutral

- Future RFC 0002 amendments require updating both the RFC table
  and the allow-list file. Future possibilities in RFC 0003
  describe a meta-check that catches the drift.

## Alternatives considered

- **Use `golang.org/x/mod/modfile` to parse.** Cleaner code, but
  pulls the tool out of the zero-dep regime. Rejected for the small
  benefit.
- **Hand-parse `go.mod` text.** No exec dependency, but the format
  has enough edge cases (block requires, `// indirect` comments,
  retraction directives) that the parser grows uncomfortably
  quickly. Rejected.
- **Custom golangci-lint linter plugin.** Idiomatic for Go-only
  rules but ties the guard to one tool's plugin model. Rejected to
  keep the RFC 0003 cross-language story consistent (every SDK has
  its own small script, none of them subclass a linter).
- **Single allow-list file in `sdk-scaffold`, copied at scaffold
  time.** Considered. Falls under RFC 0003 unresolved questions; if
  decided later, the per-repo file becomes a generated artifact.
  Not blocking this ADR.

## Revisit when

- A Go release changes the `go mod edit -json` output shape.
- RFC 0003's central-data-file question resolves; the per-repo
  file may move to `sdk-scaffold` and be regenerated on copier
  re-render.
- The reverse check (companions importing each other in violation
  of layer direction) becomes a real concern. Add a sibling tool
  rather than overload `check-l2-deps`.

## References

- RFC 0002 (layered SDK architecture) and RFC 0003 (allow-list
  guard).
- Go `cmd/go` reference: `go mod edit` JSON output.
- Existing `mise.toml` lint task chain in this repo.
