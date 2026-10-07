package vmspec

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"":           "''",
		"plain":      "'plain'",
		"a b":        "'a b'",
		"it's":       `'it'\''s'`,
		"$HOME `x`":  "'$HOME `x`'",
		"two''quote": `'two'\'''\''quote'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestParseMountSpec(t *testing.T) {
	dir := t.TempDir()

	src, dst, ro, err := parseMountSpec(dir)
	if err != nil || src != dir || dst != dir || ro {
		t.Fatalf("plain: %v %v %v %v", src, dst, ro, err)
	}
	_, dst, ro, err = parseMountSpec(dir + ":/elsewhere:ro")
	if err != nil || dst != "/elsewhere" || !ro {
		t.Fatalf("ro: %v %v %v", dst, ro, err)
	}
	for _, bad := range []string{dir + ":/x:rx", dir + ":/x:ro:extra", filepath.Join(dir, "missing")} {
		if _, _, _, err := parseMountSpec(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestWriteWinsize(t *testing.T) {
	dir := t.TempDir()
	if err := WriteWinsize(dir, 50, 160); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "winsize"))
	if string(data) != "50 160\n" {
		t.Fatalf("got %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "winsize.tmp")); !os.IsNotExist(err) {
		t.Error("temp file left behind")
	}
}

func TestBuildControlFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CBX_TEST_VAR", "it's here")

	project := t.TempDir()
	extra := t.TempDir()
	bundle := t.TempDir()

	mounts, controlDir, writeback, err := Build(bundle, Options{
		Path:       project,
		ClaudeArgs: []string{"--model", "opus", "arg with space"},
		Mounts:     []string{extra + ":/mnt/extra:ro"},
		Env:        []string{"CBX_OVERRIDE=1"},
		Rows:       40,
		Cols:       120,
	})
	if err != nil {
		t.Fatal(err)
	}

	tags := make([]string, len(mounts))
	for i, m := range mounts {
		tags[i] = m.Tag
	}
	if got := strings.Join(tags, ","); got != "project,home,claudedir,extra0" {
		t.Fatalf("mount order: %s", got)
	}
	if !mounts[3].ReadOnly || mounts[3].GuestPath != "/mnt/extra" {
		t.Errorf("extra mount: %+v", mounts[3])
	}

	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(controlDir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if read("cwd") != project+"\n" {
		t.Errorf("cwd: %q", read("cwd"))
	}
	if read("winsize") != "40 120\n" {
		t.Errorf("winsize: %q", read("winsize"))
	}
	if read("args") != "claude\n--model\nopus\narg with space\n" {
		t.Errorf("args: %q", read("args"))
	}
	if m := read("mounts"); !strings.Contains(m, "project "+project+"\n") || !strings.Contains(m, "extra0 /mnt/extra\n") {
		t.Errorf("mounts: %q", m)
	}
	env := read("env")
	for _, want := range []string{"CBX_TEST_VAR='it'\\''s here'\n", "CBX_OVERRIDE='1'\n", "PATH='/usr/local/sbin:"} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %q", want)
		}
	}
	u, _ := user.Current()
	if read("identity") != fmt.Sprintf("%s %s %s %s\n", u.Uid, u.Gid, u.Username, u.HomeDir) {
		t.Errorf("identity: %q", read("identity"))
	}

	// ~/.claude.json is created if missing, staged, and written back.
	staged := filepath.Join(bundle, "vm-home", ".claude.json")
	if data, _ := os.ReadFile(staged); string(data) != "{}" {
		t.Fatalf("staged .claude.json: %q", data)
	}
	if err := os.WriteFile(staged, []byte(`{"changed":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeback(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(home, ".claude.json")); string(data) != `{"changed":true}` {
		t.Errorf("writeback: %q", data)
	}
}

func TestBuildNoPersist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	mounts, _, _, err := Build(t.TempDir(), Options{Path: t.TempDir(), NoPersist: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].Tag != "project" {
		t.Fatalf("got %+v", mounts)
	}
}
