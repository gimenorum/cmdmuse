package complete

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWordStart(t *testing.T) {
	cases := []struct {
		line string
		pos  int
		want int
	}{
		{"ls", 2, 0},
		{"ls foo", 6, 3},
		{"ls  foo", 7, 4},
		{"cat a | gre", 11, 8},
		{"cat a && ls", 11, 9},
		{"", 0, 0},
		{"ls ", 3, 3},
		// エスケープされた空白はトークンを切らない
		{`ls my\ fi`, 9, 3},
	}
	for _, c := range cases {
		if got := wordStart(c.line, c.pos); got != c.want {
			t.Errorf("%q@%d -> %d, want %d", c.line, c.pos, got, c.want)
		}
	}
}

func TestIsCommandPosition(t *testing.T) {
	cases := []struct {
		line  string
		start int
		want  bool
	}{
		{"ls", 0, true},
		{"  ls", 2, true},
		{"ls foo", 3, false},
		{"cat a | grep", 8, true},
		{"cat a && ls", 9, true},
		{"cat a ; ls", 8, true},
		{"cat a b", 6, false},
	}
	for _, c := range cases {
		if got := isCommandPosition(c.line, c.start); got != c.want {
			t.Errorf("%q@%d -> %v, want %v", c.line, c.start, got, c.want)
		}
	}
}

func TestCommonPrefix(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"internal/", "index.md"}, "in"},
		{[]string{"same", "same"}, "same"},
		{[]string{"abc", "xyz"}, ""},
		{nil, ""},
		{[]string{"only"}, "only"},
	}
	for _, c := range cases {
		if got := CommonPrefix(c.in); got != c.want {
			t.Errorf("%v -> %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPathCompletion(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"alpha.txt", "alpine.txt", "beta.txt", ".hidden"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := paths(dir + "/al")
	if len(got) != 2 {
		t.Fatalf("alpha/alpine の2件になるはず: %v", got)
	}
	if p := CommonPrefix(got); !strings.HasSuffix(p, "/alp") {
		t.Errorf("共通接頭辞が /alp で終わるはず: %q", p)
	}

	// ディレクトリには / が付く
	sub := paths(dir + "/sub")
	if len(sub) != 1 || !strings.HasSuffix(sub[0], "/") {
		t.Errorf("ディレクトリに / が付いていない: %v", sub)
	}

	// . を打っていないなら隠しファイルは出さない
	for _, c := range paths(dir + "/") {
		if strings.Contains(filepath.Base(strings.TrimSuffix(c, "/")), ".hidden") {
			t.Errorf("隠しファイルが出た: %v", c)
		}
	}
	// . を打てば出る
	hidden := paths(dir + "/.h")
	if len(hidden) != 1 {
		t.Errorf(". を打ったら隠しファイルが出るべき: %v", hidden)
	}
}

// 空白を含むパスは、そのまま行に差し込めるようエスケープする。
func TestPathWithSpaces(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "my file.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got := paths(dir + "/my")
	if len(got) != 1 {
		t.Fatalf("%v", got)
	}
	if !strings.Contains(got[0], `\ `) {
		t.Errorf("空白がエスケープされていない: %q", got[0])
	}
}

// ~ で打ったら ~ のまま返す。絶対パスに化けると打ち直しになる。
func TestTildeStaysTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("ホームが取れない")
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) == 0 {
		t.Skip("ホームが読めない")
	}
	var name string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			name = e.Name()
			break
		}
	}
	if name == "" {
		t.Skip("隠しでないエントリが無い")
	}
	got := paths("~/" + name[:1])
	if len(got) == 0 {
		t.Fatalf("~/%s で候補が無い", name[:1])
	}
	for _, c := range got {
		if !strings.HasPrefix(c, "~/") {
			t.Errorf("~ が展開されて返っている: %q", c)
		}
	}
}

func TestCompleteCommandPosition(t *testing.T) {
	// PATH に必ずある sh を引く
	r := Complete("s", 1)
	found := false
	for _, c := range r.Candidates {
		if c == "sh" {
			found = true
		}
	}
	if !found {
		t.Errorf("コマンド位置で sh が出ない: %v", firstN(r.Candidates, 5))
	}
	if r.Start != 0 || r.End != 1 {
		t.Errorf("置換範囲が違う: %d-%d", r.Start, r.End)
	}
}

// 引数の位置ではコマンド名ではなくパスを補完する。
func TestCompleteArgumentPosition(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zzz_marker"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	line := "cat " + dir + "/zzz"
	r := Complete(line, len(line))
	if len(r.Candidates) != 1 || !strings.HasSuffix(r.Candidates[0], "zzz_marker") {
		t.Errorf("引数位置でパス補完されていない: %v", r.Candidates)
	}
	if r.Start != 4 {
		t.Errorf("置換開始位置が違う: %d (want 4)", r.Start)
	}
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
