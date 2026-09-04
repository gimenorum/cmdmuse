package expand

import "testing"

func TestFind(t *testing.T) {
	cases := []struct {
		line string
		want string // 中身。"" は見つからない
	}{
		{"AI(gitでdevelopにマージしたい)", "gitでdevelopにマージしたい"},
		{"ai(小文字でも拾う)", "小文字でも拾う"},
		{"cat server.log | AI(直近のエラー) | less", "直近のエラー"},
		{"  AI(前に空白)  ", "前に空白"},

		// 閉じていないものは未完成。打っている途中で発火させない。
		{"AI(まだ閉じてない", ""},
		{"AI(", ""},
		{"", ""},
		{"ただのコマンド", ""},

		// 入れ子の括弧
		{"AI(関数(引数)を呼ぶ方法)", "関数(引数)を呼ぶ方法"},
		// 引用符の中の括弧は数えない
		{`AI(grep "(" したい)`, `grep "(" したい`},

		// 識別子の一部は marker にしない
		{"FOOAI(x)", ""},
		{"my-ai(x)", ""},
		// 直前が記号なら拾う
		{"echo|AI(x)", "x"},
	}
	for _, c := range cases {
		got, ok := Find(c.line)
		if c.want == "" {
			if ok {
				t.Errorf("%q: 見つかってはいけない (%q)", c.line, got.Text)
			}
			continue
		}
		if !ok {
			t.Errorf("%q: 見つからなかった", c.line)
			continue
		}
		if got.Text != c.want {
			t.Errorf("%q: text=%q want %q", c.line, got.Text, c.want)
		}
	}
}

func TestReplace(t *testing.T) {
	line := "cat server.log | AI(直近のエラー) | less"
	s, ok := Find(line)
	if !ok {
		t.Fatal("見つからない")
	}
	got := Replace(line, s, "grep -i error | tail -20")
	want := "cat server.log | grep -i error | tail -20 | less"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestReplaceKeepsSurroundings(t *testing.T) {
	// 前後が空でも壊れない
	s, _ := Find("AI(x)")
	if got := Replace("AI(x)", s, "ls"); got != "ls" {
		t.Errorf("got %q", got)
	}
}

func TestHasMarker(t *testing.T) {
	if !HasMarker("AI(まだ閉じてない") {
		t.Error("閉じていなくても marker はあると判定すべき")
	}
	if HasMarker("ただのコマンド") {
		t.Error("marker が無いのにあると判定した")
	}
}
