package llm

import "testing"

func TestParseCandidatesFenced(t *testing.T) {
	raw := "以下が候補です。\n```json\n" + `[
  {"strategy":"merge-noff","command":"git merge --no-ff develop","summary":"マージコミットを残す","axis":"履歴を残す",
   "preconds":[{"desc":"git リポジトリ","probe":"git rev-parse --git-dir"}]},
  {"strategy":"merge-squash","command":"git merge --squash develop","summary":"1コミットに潰す","axis":"履歴を潰す","preconds":[]}
]` + "\n```\n以上です。"

	c, err := parseCandidates(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 2 {
		t.Fatalf("候補数 %d", len(c))
	}
	if c[0].Command != "git merge --no-ff develop" {
		t.Errorf("command=%q", c[0].Command)
	}
	if len(c[0].Preconds) != 1 || c[0].Preconds[0].Probe != "git rev-parse --git-dir" {
		t.Errorf("preconds=%v", c[0].Preconds)
	}
}

// 戦略ラベルが同じものは、コマンド文字列が違っても1つに畳む。
func TestParseCandidatesDedupByStrategy(t *testing.T) {
	raw := `[
	 {"strategy":"merge-noff","command":"git merge --no-ff develop","summary":"a","axis":"x"},
	 {"strategy":"merge-noff","command":"git merge  --no-ff  develop","summary":"b","axis":"y"},
	 {"strategy":"rebase","command":"git rebase develop","summary":"c","axis":"z"}
	]`
	c, err := parseCandidates(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 2 {
		t.Fatalf("戦略で畳めていない: %d 件 %v", len(c), c)
	}
}

func TestParseCandidatesBare(t *testing.T) {
	raw := `[{"strategy":"s","command":"ls -la","summary":"一覧","axis":"単純"}]`
	c, err := parseCandidates(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 1 || c[0].Command != "ls -la" {
		t.Errorf("got %v", c)
	}
}

// 戦略ラベルが日本語の文になって返ってきても、コマンドから作り直して畳む。
func TestParseCandidatesNormalizesStrategy(t *testing.T) {
	raw := `[
	 {"strategy":"マージ前にバックアップブランチを作成して安全網を確保する","command":"git branch backup","summary":"a","axis":"x"},
	 {"strategy":"別の日本語ラベルだが同じコマンド","command":"git branch backup2","summary":"b","axis":"y"},
	 {"strategy":"","command":"git merge --no-ff develop","summary":"c","axis":"z"}
	]`
	c, err := parseCandidates(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, cd := range c {
		if !usableStrategy(cd.Strategy) {
			t.Errorf("正規化されていない strategy: %q", cd.Strategy)
		}
	}
	if len(c) != 2 {
		t.Errorf("git branch 同士が畳まれていない: %d件", len(c))
		for _, cd := range c {
			t.Logf("  %s -> %s", cd.Strategy, cd.Command)
		}
	}
	if c[len(c)-1].Strategy != "git-merge-no-ff" {
		t.Errorf("コマンド由来のラベルが想定と違う: %q", c[len(c)-1].Strategy)
	}
}

// フラグ違いは別の戦略。ここを畳むと候補そのものが消える。
func TestStrategyFromCommandKeepsFlagDistinct(t *testing.T) {
	cases := map[string]string{
		"git merge --no-ff develop":  "git-merge-no-ff",
		"git merge --squash develop": "git-merge-squash",
		"git merge develop":          "git-merge",
		"git rebase develop":         "git-rebase",
		"rsync -av a/ b/":            "rsync-av",
		"rsync --delete a/ b/":       "rsync-delete",
		"cat ./foo.txt":              "cat",
	}
	for cmd, want := range cases {
		if got := strategyFromCommand(cmd); got != want {
			t.Errorf("%q -> %q, want %q", cmd, got, want)
		}
	}
	distinct := map[string]bool{}
	for cmd := range cases {
		distinct[strategyFromCommand(cmd)] = true
	}
	if len(distinct) != len(cases) {
		t.Errorf("ラベルが衝突している: %d コマンド -> %d ラベル", len(cases), len(distinct))
	}
}

// 生成が上限で切れて配列が閉じないことが実際に起きる。
// 揃っている要素だけでも拾えないと、全部が無駄になる。
func TestParseCandidatesSalvagesTruncated(t *testing.T) {
	raw := `[
	 {"strategy":"commit-all","command":"git commit -am 'x'","summary":"全部コミット","axis":"手早い","preconds":[]},
	 {"strategy":"add-then-commit","command":"git add -A && git commit","summary":"段階的","axis":"確認できる","preconds":[]},
	 {"strategy":"interactive","command":"git add -p","summary":"対話的に選ぶ","axis":"選`

	c, err := parseCandidates(raw)
	if err != nil {
		t.Fatalf("切れた JSON から救出できていない: %v", err)
	}
	if len(c) != 2 {
		t.Errorf("揃っている2件が取れていない: %d件", len(c))
		for _, cd := range c {
			t.Logf("  %s", cd.Command)
		}
	}
}

// 文字列の中の波括弧を数えると救出がずれる。
func TestSalvageIgnoresBracesInStrings(t *testing.T) {
	raw := `[{"strategy":"brace","command":"echo ${HOME}","summary":"波括弧を含む }","axis":"x","preconds":[]}]`
	c := salvageObjects(raw)
	if len(c) != 1 {
		t.Fatalf("%d件", len(c))
	}
	if c[0].Command != "echo ${HOME}" {
		t.Errorf("command=%q", c[0].Command)
	}
}

func TestParseCandidatesRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"", "候補はありません", "{}", "[]"} {
		if _, err := parseCandidates(raw); err == nil {
			t.Errorf("%q でエラーにならない", raw)
		}
	}
}
