// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	for src, want := range map[string]Source{
		"github.com/alex/gate4ai-rzd":                   {URL: "https://github.com/alex/gate4ai-rzd", Name: "gate4ai-rzd", Web: "https://github.com/alex/gate4ai-rzd"},
		"https://github.com/alex/gate4ai-rzd.git@v1.2.0": {URL: "https://github.com/alex/gate4ai-rzd.git", Ref: "v1.2.0", Name: "gate4ai-rzd", Web: "https://github.com/alex/gate4ai-rzd"},
		"git@github.com:alex/gate4ai-rzd.git":            {URL: "git@github.com:alex/gate4ai-rzd.git", Name: "gate4ai-rzd", Web: "https://github.com/alex/gate4ai-rzd"},
		"https://git.example.org/x/y.git":                {URL: "https://git.example.org/x/y.git", Name: "y", Web: "https://git.example.org/x/y"},
		"file:///tmp/repos/gate4ai-x.git":                {URL: "file:///tmp/repos/gate4ai-x.git", Name: "gate4ai-x"},
		"gitlab.com/g/sub/proj@main":                     {URL: "https://gitlab.com/g/sub/proj", Ref: "main", Name: "proj", Web: "https://gitlab.com/g/sub/proj"},
	} {
		got, err := Parse(src)
		if err != nil || got != want {
			t.Errorf("%s: %+v %v, want %+v", src, got, err, want)
		}
	}
	for _, bad := range []string{"rzd", "rzd@1.2.0", "./rzd", "/tmp/x.g4c", "https://x.example/y.g4c", "https://gate4.ai/", "https://git.example.org/x/y",
		"dir:/src", `C:\x\y`, "mail.g4c", "", "a b/c/d"} {
		if IsGit(bad) {
			t.Errorf("%q is taken for git", bad)
		}
	}
}

func TestCloneReadPull(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin")
	git := func(d string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", d}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.MkdirAll(origin, 0o755)
	git(origin, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(origin, "manifest.yaml"), []byte("connector: x\n"), 0o644)
	git(origin, "add", ".")
	git(origin, "commit", "-q", "-m", "one")
	git(origin, "tag", "v1")

	plain := filepath.Join(dir, "plain")
	os.MkdirAll(plain, 0o755)
	if st := Read(plain); st.Repo || st.Dirty != nil {
		t.Fatalf("%+v", st)
	}
	src, err := Parse("file://" + origin + "@v1")
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "clone")
	if err := Clone(context.Background(), src, dest); err != nil {
		t.Fatal(err)
	}
	st := Read(dest)
	if !st.Repo || st.Remote != "file://"+origin || st.Dirty == nil || *st.Dirty || st.Commit == "" {
		t.Fatalf("%+v", st)
	}
	if err := Clone(context.Background(), src, dest); err == nil {
		t.Fatal("clone over an existing folder")
	}
	// Чистый клон подтягивается; грязный — нет.
	os.WriteFile(filepath.Join(origin, "README.md"), []byte("hi\n"), 0o644)
	git(origin, "add", ".")
	git(origin, "commit", "-q", "-m", "two")
	// Клон по тегу стоит на detached HEAD: pull не сработает — проверяем ветку.
	dest2 := filepath.Join(dir, "clone2")
	src2, _ := Parse("file://" + origin)
	if err := Clone(context.Background(), src2, dest2); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(origin, "X"), []byte("x\n"), 0o644)
	git(origin, "add", ".")
	git(origin, "commit", "-q", "-m", "three")
	if pulled, note := PullIfClean(context.Background(), dest2); !pulled || note != "" {
		t.Fatal(pulled, note)
	}
	if _, err := os.Stat(filepath.Join(dest2, "X")); err != nil {
		t.Fatal("not pulled")
	}
	os.WriteFile(filepath.Join(dest2, "manifest.yaml"), []byte("connector: y\n"), 0o644)
	if pulled, note := PullIfClean(context.Background(), dest2); pulled || note == "" {
		t.Fatal(pulled, note)
	}
	if pulled, note := PullIfClean(context.Background(), plain); pulled || note != "" {
		t.Fatal(pulled, note)
	}
}
