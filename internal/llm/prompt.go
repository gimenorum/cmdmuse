package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/gimenorum/cmdmuse/internal/core"
)

// candidateRules は候補列挙と深堀りの両方で使う共通ルール。
// 呼び出しが別なので、片方から「前と同じ」と参照させてはいけない。
const candidateRules = `## 各候補に付けるもの

- strategy: 戦略ラベル。**英小文字とハイフンのみ**。日本語や空白や文を入れてはいけない
            (良い: no-ff-merge / squash-merge / backup-branch)
            (悪い: マージ前にバックアップを作る / backup branch)
            これが重複したら同じ候補とみなして捨てられる
- command:  実際に実行するコマンド1行
- summary:  この戦略が何をするかを日本語20〜40字で
- axis:     他とどの軸で違うのか。日本語15字以内
            (例: 履歴を残す / 移植性が高い / 再帰的 / 破壊的)
            **候補ごとに必ず違う語にする。**同じ axis が並ぶのは
            候補が実質同じか、軸を言語化できていない証拠なので見直すこと
- preconds: この候補が成立する前提条件。**必ず配列で入れる**

## preconds の書き方

前提が崩れていたら候補ごと無意味になる条件だけを挙げます。
probe は**必ず副作用のない読み取り専用コマンド**にしてください。
ファイルを書き換える、削除する、ネットワークに変更を加えるコマンドは厳禁です。

  {"desc": "カレントが git リポジトリ", "probe": "git rev-parse --git-dir"}
  {"desc": "develop ブランチが存在", "probe": "git branch --list develop"}
  {"desc": "rename コマンドが使える", "probe": "command -v rename"}

probe に使ってよいもの: test, command -v, which, ls, stat, git rev-parse,
git branch --list, git status --porcelain, git log, find (-delete/-exec なし),
grep, cat, head, wc など。
パイプ・リダイレクト・&&・; は使わないでください。1コマンドだけ書きます。

**使うコマンドが手元にあるかの確認は、ほぼ常に入れてください。**
道具を散らすほど「その環境に無い」ことが起きます。paste, xargs, awk, sed, perl,
rename, jq のような外部コマンドを使うなら "command -v <名前>" を必ず1つ入れます。
シェル組み込み (echo, printf, cd, test) だけで済む候補には不要です。

ファイルやディレクトリを引数に取るなら、その存在も前提に挙げます。
標準入力を受け取るだけの断片には、入力ファイルの前提は要りません。

本当に前提が無い場合だけ空配列にします。

## 出力

JSON配列のみを出力します。説明文やコードフェンスは付けません。
候補は3〜5個。多様性を優先し、思いつかなければ少なくて構いません。

[{"strategy":"...","command":"...","summary":"...","axis":"...","preconds":[{"desc":"...","probe":"..."}]}]`

const candidateSystem = `あなたはシェルコマンドの専門家です。ユーザーの目的に対して、
**アプローチが本質的に異なる**選択肢を列挙します。

## 入力が「名前」だけのとき

入力が動作を表さず、**コマンド名・ツール名・ファイル名だけ**のことがあります
(例: "claude code" / "docker" / "server.log")。
このとき求められているのは、その名前を**文字列として加工すること**ではありません。
まず疑うべき意図はこの3つです。

- **起動したい** (すでに入っているなら最有力)
- **導入したい** (入っていないなら)
- **在りかを知りたい / 状態を見たい**

悪い例 (却下):
  echo 'claude code' | sed 's/ /_/g'      ← 名前を文字列として加工している
  printf "claude code" | wc -w

良い例 ("claude code" に対して):
  claude                                  戦略: そのまま起動する
  command -v claude || npm i -g ...       戦略: 無ければ導入する
  claude --help                           戦略: 使い方を見る

## 最重要ルール

まず、**ユーザーが書いた目的をそのまま満たすこと**。
勝手に用途を広げたり、近いけれど別のことをするコマンドを並べてはいけません。
「名前を1行にまとめたい」なら整形であって、集計ではありません。
全候補が目的を満たした上で、やり方が違っている必要があります。

そして、同じやり方の書き方違いを複数出してはいけません。求められているのは
「同じ目的を達成する、考え方の違うやり方」です。

悪い例 (却下):
  git merge develop
  git merge  develop
  git merge "develop"        ← 全部同じ戦略。1つに数える

良い例:
  git merge --no-ff develop  戦略: マージコミットを残す
  git merge --squash develop 戦略: 履歴を1コミットに潰す
  git rebase develop         戦略: 履歴を直線化する

## 使うコマンド自体を散らすこと

**同じコマンドのフラグ違いばかりを並べてはいけません。**
同じことを実現できる道具は普通いくつもあります。まず道具を変えて考えてください。

悪い例 (却下):
  xargs -I {} echo {}
  xargs -I {} basename {}
  xargs | tr ' ' '\n'        ← 全部 xargs。道具が散っていない

良い例 (改行を空白に変えて1行にする):
  tr '\n' ' '                戦略: 文字単位で置換する
  paste -sd' ' -             戦略: 行を連結する専用コマンド
  awk '{printf "%s ", $0}'   戦略: 整形言語で書く
  xargs echo                 戦略: 引数として展開させる

シェル組み込み・coreutils・awk/sed/perl・専用ツールのように、
**系統の違う道具**から選ぶと自然に散ります。
どうしても同じコマンドしか無い場合だけ、フラグ違いを出してよいです。

` + candidateRules

func buildContextBlock(sc core.SessionContext) string {
	var b strings.Builder
	b.WriteString("## 実行環境\n")
	fmt.Fprintf(&b, "OS: %s\n", sc.OS)
	if sc.Shell != "" {
		fmt.Fprintf(&b, "シェル: %s\n", sc.Shell)
	}
	if sc.Cwd != "" {
		fmt.Fprintf(&b, "カレントディレクトリ: %s\n", sc.Cwd)
	}
	if len(sc.Recent) > 0 {
		b.WriteString("\n## 直前のセッション\n```\n")
		b.WriteString(strings.Join(sc.Recent, "\n"))
		b.WriteString("\n```\n")
		if sc.HasExit && sc.LastExit != 0 {
			fmt.Fprintf(&b, "直前のコマンドは終了コード %d で失敗しています。"+
				"これを踏まえた候補を優先してください。\n", sc.LastExit)
		}
	}
	return b.String()
}

// LineContext は AI(...) が書かれていた行の前後。
// 生成するのは AI(...) を置き換える断片であって、行全体ではない。
type LineContext struct {
	Prefix string // AI( の前
	Suffix string // ) の後
}

func (l LineContext) block() string {
	if l.Prefix == "" && l.Suffix == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## 重要: 行の一部だけを書くこと\n\n")
	b.WriteString("ユーザーが打っている行はこうなっています。\n\n```\n")
	b.WriteString(l.Prefix + "【ここにあなたの出力が入る】" + l.Suffix)
	b.WriteString("\n```\n\n")
	b.WriteString("command には**【ここ】に入る断片だけ**を書いてください。行全体を書いてはいけません。\n")
	if p := strings.TrimSpace(l.Prefix); strings.HasSuffix(p, "|") {
		b.WriteString("前段がパイプで繋がっているので、**標準入力を受け取る形**で書きます。\n")
		b.WriteString("入力元をもう一度書いてはいけません")
		b.WriteString("(例: `ls | AI(...)` に対して `ls | tr ...` と書くと ls が二重になる)。\n")
	}
	if s := strings.TrimSpace(l.Suffix); strings.HasPrefix(s, "|") {
		b.WriteString("後段にパイプが続くので、**標準出力に流す**形で書きます。\n")
	}
	return b.String()
}

// knownCommands は目的の中に出てくる語のうち、実際に PATH にあるものを調べる。
// 「claude code」のような入力で、起動できるのか導入が要るのかをモデルに推測させない。
func knownCommands(goal string) string {
	var found, missing []string
	seen := map[string]bool{}
	for _, w := range strings.Fields(goal) {
		w = strings.Trim(w, "\"'`.,()[]{}、。")
		if len(w) < 2 || len(w) > 40 || seen[w] {
			continue
		}
		// 日本語が混ざる語はコマンド名ではない。
		if strings.IndexFunc(w, func(r rune) bool { return r > 127 }) >= 0 {
			continue
		}
		seen[w] = true
		if p, err := exec.LookPath(w); err == nil {
			found = append(found, w+" → "+p)
		} else {
			missing = append(missing, w)
		}
	}
	if len(found) == 0 && len(missing) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## この環境での実在確認 (事実。推測しないこと)\n")
	for _, f := range found {
		b.WriteString("- " + f + "  ← 入っている。起動する候補を優先すること\n")
	}
	for _, m := range missing {
		b.WriteString("- " + m + " は PATH に無い\n")
	}
	return b.String()
}

// Candidates は自然言語の目的から、アプローチの異なる候補を列挙する。
func (c *Client) Candidates(ctx context.Context, goal string, sc core.SessionContext, lc LineContext) ([]core.Candidate, error) {
	user := buildContextBlock(sc) + lc.block() + knownCommands(goal) + "\n## 目的\n" + goal
	raw, err := c.Chat(ctx, []Message{
		{Role: "system", Content: candidateSystem},
		{Role: "user", Content: user},
	}, 0.4, 4096)
	if err != nil {
		return nil, err
	}
	return parseCandidates(raw)
}

const followupSystem = `あなたはシェルコマンドの専門家です。
ユーザーが特定の候補について追加の質問をしています。
その候補を踏まえた上で、質問に答える**新しい候補**を列挙してください。

もとの候補と同じアプローチを繰り返さず、質問の観点から見て
本質的に異なるやり方を並べます。

` + candidateRules

// Followup は選択中の候補を文脈に、追加質問から派生候補を作る。
func (c *Client) Followup(ctx context.Context, goal string, base core.Candidate, question string, sc core.SessionContext, lc LineContext) ([]core.Candidate, error) {
	user := fmt.Sprintf("%s%s\n## もとの目的\n%s\n\n## 選択中の候補\n```\n%s\n```\n%s\n\n## 質問\n%s",
		buildContextBlock(sc), lc.block(), goal, base.Command, base.Summary, question)
	raw, err := c.Chat(ctx, []Message{
		{Role: "system", Content: followupSystem},
		{Role: "user", Content: user},
	}, 0.4, 4096)
	if err != nil {
		return nil, err
	}
	return parseCandidates(raw)
}

const explainSystem = `あなたはシェルコマンドの解説者です。
与えられたコマンドを日本語で解説します。

## 絶対のルール

フラグの意味は、下に提示された「実定義」だけを根拠にしてください。
提示されていないフラグについて意味を推測して書いてはいけません。
実定義が空のフラグは、意味を断定せず「定義を取得できませんでした」と書きます。

あなたが自分の判断で書いてよいのは、
- コマンド全体が何をするか
- なぜこの組み合わせなのか
- 他の候補と比べてどういう時に選ぶべきか
- 実行して困る点、戻し方
に限ります。

## 出力

見出しや箇条書きを使わず、200字程度の平文で書いてください。
「〜します」調。冗長な前置きは不要です。`

// Explain は候補の解説を作る。flags には --help/man から取った実定義だけを渡し、
// モデルにフラグの意味を思い出させない。
func (c *Client) Explain(ctx context.Context, cand core.Candidate, others []core.Candidate) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "## 解説するコマンド\n```\n%s\n```\n戦略: %s\n\n", cand.Command, cand.Summary)

	b.WriteString("## フラグの実定義 (これだけを根拠にすること)\n")
	if len(cand.Flags) == 0 {
		b.WriteString("(このコマンドにフラグはありません)\n")
	}
	for _, f := range cand.Flags {
		if f.Def == "" {
			fmt.Fprintf(&b, "%s: (定義を取得できませんでした)\n", f.Flag)
		} else {
			fmt.Fprintf(&b, "%s: %s\n", f.Flag, f.Def)
		}
	}

	if len(others) > 0 {
		b.WriteString("\n## 比較対象の他候補\n")
		for _, o := range others {
			if o.Strategy == cand.Strategy {
				continue
			}
			fmt.Fprintf(&b, "- %s  (%s)\n", o.Command, o.Axis)
		}
	}

	if fails := cand.FailedPreconds(); len(fails) > 0 {
		b.WriteString("\n## この候補は現在の環境では前提を満たしていません\n")
		for _, p := range fails {
			fmt.Fprintf(&b, "- %s\n", p.Desc)
		}
		b.WriteString("この点にも触れてください。\n")
	}

	return c.Chat(ctx, []Message{
		{Role: "system", Content: explainSystem},
		{Role: "user", Content: b.String()},
	}, 0.3, 700)
}

// parseCandidates はモデル出力から JSON 配列を取り出す。
// コードフェンスや前後の地の文が混ざっても拾えるようにしている。
func parseCandidates(raw string) ([]core.Candidate, error) {
	s := extractJSONArray(raw)
	if s == "" {
		return nil, fmt.Errorf("JSON配列が見つかりません: %s", truncate(raw, 160))
	}
	var cands []core.Candidate
	if err := json.Unmarshal([]byte(s), &cands); err != nil {
		// 生成が上限で切れると配列が閉じない。最後の1件を捨ててでも
		// 揃っているぶんは使う。全部無駄にするよりましなので。
		cands = salvageObjects(s)
		if len(cands) == 0 {
			return nil, fmt.Errorf("JSON解釈に失敗: %w", err)
		}
	}

	seen := map[string]bool{}
	out := make([]core.Candidate, 0, len(cands))
	for _, c := range cands {
		c.Command = strings.TrimSpace(c.Command)
		c.Strategy = strings.TrimSpace(strings.ToLower(c.Strategy))
		if c.Command == "" {
			continue
		}
		// 戦略ラベルが使えない形なら捨ててコマンドから作り直す。
		// ラベルが文になると重複判定が効かなくなるので、ここで正規化しておく。
		if !usableStrategy(c.Strategy) {
			c.Strategy = strategyFromCommand(c.Command)
		}
		key := c.Strategy
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("有効な候補がありません")
	}
	return out, nil
}

// salvageObjects は壊れた JSON 配列から、閉じている要素だけを取り出す。
// 文字列リテラル内の波括弧を数えないよう、エスケープと引用符を追跡する。
func salvageObjects(s string) []core.Candidate {
	var out []core.Candidate
	depth, start := 0, -1
	inStr, esc := false, false

	for i, r := range s {
		switch {
		case esc:
			esc = false
		case r == '\\' && inStr:
			esc = true
		case r == '"':
			inStr = !inStr
		case inStr:
			// 文字列の中の括弧は数えない
		case r == '{':
			if depth == 0 {
				start = i
			}
			depth++
		case r == '}':
			depth--
			if depth == 0 && start >= 0 {
				var c core.Candidate
				if json.Unmarshal([]byte(s[start:i+1]), &c) == nil && strings.TrimSpace(c.Command) != "" {
					out = append(out, c)
				}
				start = -1
			}
			if depth < 0 {
				depth = 0
			}
		}
	}
	return out
}

// usableStrategy は重複判定のキーとして使える形か。
// 文章になっているラベルは、意味が同じでも文字列が違うため畳めない。
func usableStrategy(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// strategyFromCommand はコマンドから戦略ラベルを作る。
//
// コマンド名とサブコマンドだけでは不十分で、必ず最初のフラグまで含める。
// git merge --no-ff と git merge --squash はやり方が違うので、
// 同じラベルになって畳まれてしまうと候補が消える。
func strategyFromCommand(cmd string) string {
	var words []string
	var flag string
	for _, f := range strings.Fields(cmd) {
		if strings.HasPrefix(f, "-") {
			if flag == "" && f != "-" && f != "--" {
				if i := strings.Index(f, "="); i > 0 {
					f = f[:i]
				}
				flag = strings.TrimLeft(f, "-")
			}
			continue
		}
		if strings.ContainsAny(f, "/\\.$*'\"") {
			continue
		}
		if len(words) < 2 {
			words = append(words, f)
		}
	}
	parts := words
	if flag != "" {
		parts = append(parts, flag)
	}
	if len(parts) == 0 {
		return strings.ToLower(strings.Join(strings.Fields(cmd), "-"))
	}
	return sanitizeLabel(strings.Join(parts, "-"))
}

func sanitizeLabel(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func extractJSONArray(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if j := strings.Index(rest, "\n"); j >= 0 {
			rest = rest[j+1:]
		}
		if k := strings.Index(rest, "```"); k >= 0 {
			rest = rest[:k]
		}
		s = strings.TrimSpace(rest)
	}
	start := strings.Index(s, "[")
	if start < 0 {
		// 配列にすらならなかった場合でも、オブジェクトが並んでいれば拾える。
		if i := strings.Index(s, "{"); i >= 0 {
			return s[i:]
		}
		return ""
	}
	// 末尾から ] を探すと "preconds":[] の ] を拾ってオブジェクトの途中で切れる。
	// 開き括弧に対応する閉じ括弧を数えて特定する。
	if end := matchBracket(s, start); end > start {
		return s[start : end+1]
	}
	// 対応する ] が無いのは生成が途中で切れた場合。残り全部を救出に回す。
	return s[start:]
}

// matchBracket は s[open] の '[' に対応する ']' の位置を返す。無ければ -1。
func matchBracket(s string, open int) int {
	depth := 0
	inStr, esc := false, false
	for i, r := range s[open:] {
		switch {
		case esc:
			esc = false
		case r == '\\' && inStr:
			esc = true
		case r == '"':
			inStr = !inStr
		case inStr:
		case r == '[':
			depth++
		case r == ']':
			depth--
			if depth == 0 {
				return open + i
			}
		}
	}
	return -1
}
