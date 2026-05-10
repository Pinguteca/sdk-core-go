// check-l2-deps verifies that the root go.mod's direct dependencies are
// all on the Layer 2 allow list defined by RFC 0002 (per-language
// allow list) and RFC 0003 (CI guard contract). Implementation choice
// is recorded in ADR 0011: shell to `go mod edit -json` so the tool
// itself stays zero-dep, and ship the allow list as a plain-text file
// next to this program.
//
// Run:
//
//	go run ./tools/check-l2-deps
//
// Flags:
//
//	-mod    path to the go.mod under inspection (default "go.mod")
//	-allow  path to the allow-list file
//	        (default "tools/check-l2-deps/allowlist.txt")
//
// Exit codes:
//
//	0  every direct dep is on the allow list.
//	1  one or more direct deps are not on the allow list.
//	2  a usage / IO / parse error occurred.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

const (
	exitOK         = 0
	exitForbidden  = 1
	exitUsageError = 2
)

// goMod mirrors the subset of `go mod edit -json` output that the guard
// needs. The full schema includes module/path, go version, retract
// blocks, etc., none of which are relevant here.
type goMod struct {
	Require []requireEntry `json:"Require"`
}

type requireEntry struct {
	Path     string `json:"Path"`
	Version  string `json:"Version"`
	Indirect bool   `json:"Indirect"`
}

func main() {
	modPath := flag.String("mod", "go.mod", "path to the go.mod under inspection")
	allowPath := flag.String("allow", "tools/check-l2-deps/allowlist.txt",
		"path to the allow-list file (one entry per line, # comments, /* prefix glob)")
	flag.Parse()

	os.Exit(run(context.Background(), *modPath, *allowPath, os.Stdout, os.Stderr))
}

func run(ctx context.Context, modPath, allowPath string, stdout, stderr io.Writer) int {
	mod, err := readGoMod(ctx, modPath, stderr)
	if err != nil {
		writef(stderr, "check-l2-deps: read %s: %v\n", modPath, err)
		return exitUsageError
	}

	allow, err := readAllowList(allowPath)
	if err != nil {
		writef(stderr, "check-l2-deps: read %s: %v\n", allowPath, err)
		return exitUsageError
	}

	violations := findViolations(mod, allow)
	if len(violations) == 0 {
		writef(stdout, "check-l2-deps: %s: all direct dependencies are on the allow list.\n", modPath)
		return exitOK
	}

	for _, dep := range violations {
		writef(stderr, "ERROR: dependency %q is not on the L2 allow list.\n", dep)
	}
	writef(stderr, "\nAllow list (%s):\n", allowPath)
	for _, entry := range allow {
		writef(stderr, "  - %s\n", entry)
	}
	writef(stderr, "\nIf this dep belongs in Layer 2, add it to the allow list and update RFC 0002.\n")
	writef(stderr, "If it belongs in Layer 3, move the package that uses it into a companion sub-module.\n")
	return exitForbidden
}

func readGoMod(ctx context.Context, path string, stderr io.Writer) (*goMod, error) {
	cmd := exec.CommandContext(ctx, "go", "mod", "edit", "-json", path)
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go mod edit -json %s: %w", path, err)
	}
	var m goMod
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("parse go mod edit -json output: %w", err)
	}
	return &m, nil
}

func readAllowList(path string) ([]string, error) {
	raw, err := os.ReadFile(path) //#nosec G304 -- caller-supplied path is the API of this tool; no untrusted input.
	if err != nil {
		return nil, fmt.Errorf("read allow list: %w", err)
	}
	var entries []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		entries = append(entries, trimmed)
	}
	return entries, nil
}

func findViolations(mod *goMod, allow []string) []string {
	var bad []string
	for _, r := range mod.Require {
		if r.Indirect {
			continue
		}
		if !matchAny(r.Path, allow) {
			bad = append(bad, r.Path)
		}
	}
	return bad
}

func matchAny(dep string, allow []string) bool {
	for _, entry := range allow {
		if matches(dep, entry) {
			return true
		}
	}
	return false
}

// matches reports whether dep is admitted by entry. An entry ending in
// "/*" is a path-prefix glob (the "/*" suffix is stripped and the
// remainder must be a strict prefix of dep, with the prefix boundary at
// a "/" character to avoid `golang.org/x/*` admitting `golang.org/xfoo`).
// Any other entry must equal dep exactly.
func matches(dep, entry string) bool {
	if before, ok := strings.CutSuffix(entry, "/*"); ok {
		prefix := before
		return dep == prefix || strings.HasPrefix(dep, prefix+"/")
	}
	return dep == entry
}

// writef wraps [fmt.Fprintf] for cases where the caller has chosen to
// ignore I/O errors against stdout/stderr. errcheck flags the bare call
// because Fprintf returns (n, error); the assignment makes the choice
// explicit.
func writef(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
