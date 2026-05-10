package main

import "testing"

func TestMatches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		dep, entry string
		want       bool
	}{
		{"connectrpc.com/connect", "connectrpc.com/connect", true},
		{"connectrpc.com/connect", "connectrpc.com/connect/v2", false},
		{"golang.org/x/oauth2", "golang.org/x/*", true},
		{"golang.org/x/oauth2/clientcredentials", "golang.org/x/*", true},
		{"golang.org/x", "golang.org/x/*", true},
		{"golang.org/xfoo", "golang.org/x/*", false},
		{"google.golang.org/genproto/googleapis/rpc", "google.golang.org/genproto/googleapis/*", true},
		{"google.golang.org/genproto/googleapis", "google.golang.org/genproto/googleapis/*", true},
		{"google.golang.org/genprotofake", "google.golang.org/genproto/googleapis/*", false},
		{"github.com/example/foo", "connectrpc.com/connect", false},
	}
	for _, c := range cases {
		got := matches(c.dep, c.entry)
		if got != c.want {
			t.Errorf("matches(%q, %q) = %v, want %v", c.dep, c.entry, got, c.want)
		}
	}
}

func TestFindViolations_AllPermitted(t *testing.T) {
	t.Parallel()
	mod := &goMod{Require: []requireEntry{
		{Path: "connectrpc.com/connect", Indirect: false},
		{Path: "golang.org/x/oauth2", Indirect: false},
		{Path: "google.golang.org/protobuf", Indirect: false},
	}}
	allow := []string{
		"connectrpc.com/connect",
		"golang.org/x/*",
		"google.golang.org/protobuf",
	}
	if got := findViolations(mod, allow); len(got) != 0 {
		t.Errorf("violations = %v, want none", got)
	}
}

func TestFindViolations_SkipsIndirect(t *testing.T) {
	t.Parallel()
	mod := &goMod{Require: []requireEntry{
		{Path: "github.com/example/banned", Indirect: true},
	}}
	allow := []string{"connectrpc.com/connect"}
	if got := findViolations(mod, allow); len(got) != 0 {
		t.Errorf("violations = %v, want none (indirect deps must be ignored)", got)
	}
}

func TestFindViolations_FlagsForbidden(t *testing.T) {
	t.Parallel()
	mod := &goMod{Require: []requireEntry{
		{Path: "connectrpc.com/connect", Indirect: false},
		{Path: "github.com/example/foo", Indirect: false},
		{Path: "github.com/example/bar", Indirect: false},
	}}
	allow := []string{"connectrpc.com/connect"}
	got := findViolations(mod, allow)
	want := []string{"github.com/example/foo", "github.com/example/bar"}
	if len(got) != len(want) {
		t.Fatalf("violations = %v, want %v", got, want)
	}
	for i, dep := range want {
		if got[i] != dep {
			t.Errorf("violations[%d] = %q, want %q", i, got[i], dep)
		}
	}
}
