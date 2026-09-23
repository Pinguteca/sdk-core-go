// workspace-run executes one command in every module listed in go.work,
// then reports which modules failed.
//
// It exists because a `./...` package pattern resolves against the
// current module, not the workspace. `go build`, `go test` and
// `golangci-lint run` at the workspace root therefore all covered only
// the root module, leaving nine companion modules (breaker, caching,
// compression, ergo, hedge, logging, otel, presets,
// transport/mtls/pkcs12) unbuilt, untested and unlinted in CI. That is
// how a data race in hedge and two unbounded reads in caching reached
// main.
//
// Implementation follows ADR 0011 and check-l2-deps: shell out so the
// tool stays zero-dep, and read the module list from go.work rather
// than duplicating it here, so adding a module to the workspace is
// enough to bring it under CI.
//
// Run:
//
//	go run ./tools/workspace-run -- test -race -count=1 ./...
//	go run ./tools/workspace-run -- build ./...
//	go run ./tools/workspace-run -cmd golangci-lint -- run ./...
//
// Flags:
//
//	-work  path to the go.work under inspection (default "go.work")
//	-cmd   executable to invoke in each module (default "go")
//
// Exit codes:
//
//	0  the command succeeded in every module.
//	1  the command failed in one or more modules.
//	2  a usage / IO / parse error occurred.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	exitOK         = 0
	exitFailed     = 1
	exitUsageError = 2
)

// workFile is the subset of `go work edit -json` this tool consumes.
type workFile struct {
	Use []struct {
		DiskPath string `json:"DiskPath"`
	} `json:"Use"`
}

func main() {
	workPath := flag.String("work", "go.work", "path to the go.work under inspection")
	command := flag.String("cmd", "go", "executable to invoke in each module")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "workspace-run: no arguments given")
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/workspace-run -- test -race ./...")
		os.Exit(exitUsageError)
	}

	ctx := context.Background()
	modules, err := readModules(ctx, *workPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "workspace-run: %v\n", err)
		os.Exit(exitUsageError)
	}

	root := filepath.Dir(*workPath)
	var failed []string
	for _, rel := range modules {
		dir := filepath.Join(root, rel)
		fmt.Printf("==> %s: %s %s\n", rel, *command, join(args))
		if runErr := run(ctx, dir, *command, args); runErr != nil {
			fmt.Fprintf(os.Stderr, "workspace-run: %s: %v\n", rel, runErr)
			failed = append(failed, rel)
		}
	}

	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "\nworkspace-run: %d of %d module(s) failed: %s\n",
			len(failed), len(modules), join(failed))
		os.Exit(exitFailed)
	}
	fmt.Printf("\nworkspace-run: %d module(s) ok\n", len(modules))
	os.Exit(exitOK)
}

// readModules returns the DiskPath of every `use` directive in the
// workspace file, in declaration order.
func readModules(ctx context.Context, workPath string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "go", "work", "edit", "-json", workPath)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go work edit -json %s: %w", workPath, err)
	}
	var wf workFile
	if unmarshalErr := json.Unmarshal(out, &wf); unmarshalErr != nil {
		return nil, fmt.Errorf("parse go work edit output: %w", unmarshalErr)
	}
	if len(wf.Use) == 0 {
		return nil, fmt.Errorf("%s lists no modules", workPath)
	}
	paths := make([]string, 0, len(wf.Use))
	for _, u := range wf.Use {
		paths = append(paths, u.DiskPath)
	}
	return paths, nil
}

// run invokes `command args...` in dir, streaming output to this process
// so failures stay readable in CI logs.
func run(ctx context.Context, dir, command string, args []string) error {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", command, join(args), err)
	}
	return nil
}

func join(parts []string) string {
	return strings.Join(parts, " ")
}
