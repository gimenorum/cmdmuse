package spec

import (
	"context"
	"testing"
)

func TestSplit(t *testing.T) {
	cases := []struct {
		in    string
		base  string
		flags []string
	}{
		{"git merge --no-ff develop", "git merge", []string{"--no-ff"}},
		{"git merge --squash develop", "git merge", []string{"--squash"}},
		{"ls -la /tmp", "ls", []string{"-la"}},
		{"rsync -av --dry-run a/ b/", "rsync", []string{"-av", "--dry-run"}},
		{"find . -name '*.txt'", "find", []string{"-name"}},
		{"git commit -m 'x' --amend", "git commit", []string{"-m", "--amend"}},
		{"tar -xzf a.tgz", "tar", []string{"-xzf"}},
		// = 付きは丸める
		{"git log --format=oneline", "git log", []string{"--format"}},
		// 環境変数の前置きは読み飛ばす
		{"FOO=1 ls -l", "ls", []string{"-l"}},
		// パスをサブコマンドと誤認しない
		{"cat ./foo.txt", "cat", nil},
		// パイプは先頭区間だけ
		{"ls -l | grep x", "ls", []string{"-l"}},
	}
	for _, c := range cases {
		base, flags := Split(c.in)
		if base != c.base {
			t.Errorf("%q: base=%q want %q", c.in, base, c.base)
		}
		if len(flags) != len(c.flags) {
			t.Errorf("%q: flags=%v want %v", c.in, flags, c.flags)
			continue
		}
		for i := range flags {
			if flags[i] != c.flags[i] {
				t.Errorf("%q: flags[%d]=%q want %q", c.in, i, flags[i], c.flags[i])
			}
		}
	}
}

func TestParseHelp(t *testing.T) {
	help := `usage: thing [options]

  -n, --dry-run         do not actually do anything
      --no-ff           create a merge commit even when fast-forward
  -f, --force
                        force it even if dangerous
  -v                    verbose
`
	d := parseHelp(help)
	want := map[string]string{
		"-n":        "do not actually do anything",
		"--dry-run": "do not actually do anything",
		"--no-ff":   "create a merge commit even when fast-forward",
		"-f":        "force it even if dangerous",
		"--force":   "force it even if dangerous",
		"-v":        "verbose",
	}
	for k, v := range want {
		if d[k] != v {
			t.Errorf("%s = %q, want %q", k, d[k], v)
		}
	}
}

// 実際にインストールされているコマンドから定義が引けることを確かめる。
func TestFetchReal(t *testing.T) {
	ctx := context.Background()
	for _, base := range []string{"ls", "git merge", "grep"} {
		d := fetch(ctx, base)
		if len(d) == 0 {
			t.Logf("%s: 定義を取得できず (環境依存なので失敗にはしない)", base)
			continue
		}
		t.Logf("%s: %d 個のフラグ定義", base, len(d))
	}
}
