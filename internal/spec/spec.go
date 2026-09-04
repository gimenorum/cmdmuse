// Package spec はコマンドのフラグ定義を --help / man から取得する。
//
// ここで取れた定義だけを LLM に渡し、フラグの意味の生成を禁じることで
// 「それらしいが間違った解説」を構造的に防ぐ。取得できなかったフラグは
// 空のまま渡し、モデルには「不明」と書かせる。
package spec

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gimenorum/cmdmuse/internal/core"
)

type Lookup struct {
	mu    sync.Mutex
	cache map[string]map[string]string // "git merge" -> {"--no-ff": "..."}
}

func NewLookup() *Lookup {
	return &Lookup{cache: map[string]map[string]string{}}
}

var flagLine = regexp.MustCompile(`^\s{1,10}(-{1,2}[A-Za-z0-9][A-Za-z0-9-]*(?:\s*,\s*-{1,2}[A-Za-z0-9][A-Za-z0-9-]*)*)`)
var flagToken = regexp.MustCompile(`-{1,2}[A-Za-z0-9][A-Za-z0-9-]*`)

// Annotate は候補が使っているフラグを洗い出し、定義を引いて書き戻す。
func (l *Lookup) Annotate(ctx context.Context, c *core.Candidate) {
	base, flags := Split(c.Command)
	if len(flags) == 0 {
		c.Flags = nil
		return
	}
	defs := l.defs(ctx, base)
	out := make([]core.FlagDoc, 0, len(flags))
	for _, f := range flags {
		if d, ok := defs[f]; ok {
			out = append(out, core.FlagDoc{Flag: f, Def: d})
			continue
		}
		// -lah のような束ねた短フラグは、そのままでは定義が無い。
		// 分解して全部に定義があるときだけ束ねと判断する。
		if parts, ok := unbundle(f, defs); ok {
			out = append(out, parts...)
			continue
		}
		out = append(out, core.FlagDoc{Flag: f})
	}
	c.Flags = out
}

// unbundle は -lah を -l -a -h に分解する。
// 全ての文字に定義がある場合だけ束ねとみなす。find の -name のような
// 単一ハイフンの長オプションを誤って分解しないための条件。
func unbundle(flag string, defs map[string]string) ([]core.FlagDoc, bool) {
	if len(flag) < 3 || strings.HasPrefix(flag, "--") || !strings.HasPrefix(flag, "-") {
		return nil, false
	}
	var out []core.FlagDoc
	for _, r := range flag[1:] {
		single := "-" + string(r)
		d, ok := defs[single]
		if !ok {
			return nil, false
		}
		out = append(out, core.FlagDoc{Flag: single, Def: d})
	}
	return out, true
}

// Split はコマンド行を「定義を引く単位」とフラグ列に分ける。
// "git merge --no-ff develop" -> ("git merge", ["--no-ff"])
func Split(cmdline string) (base string, flags []string) {
	// パイプがあれば先頭の区間だけを見る。
	if i := strings.IndexAny(cmdline, "|;&"); i > 0 {
		cmdline = cmdline[:i]
	}
	fields := strings.Fields(cmdline)
	if len(fields) == 0 {
		return "", nil
	}
	// 環境変数の前置き (FOO=bar cmd) を読み飛ばす。
	for len(fields) > 0 && strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "-") {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return "", nil
	}

	base = fields[0]
	rest := fields[1:]
	// 第2トークンがフラグでなければサブコマンドとみなす。
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") && looksLikeSubcommand(rest[0]) {
		base += " " + rest[0]
		rest = rest[1:]
	}

	seen := map[string]bool{}
	for _, f := range rest {
		if !strings.HasPrefix(f, "-") || f == "-" || f == "--" {
			continue
		}
		// --opt=value は --opt に丸める。
		if i := strings.Index(f, "="); i > 0 {
			f = f[:i]
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		flags = append(flags, f)
	}
	return base, flags
}

// looksLikeSubcommand はパスやファイル名をサブコマンドと誤認しないための判定。
func looksLikeSubcommand(s string) bool {
	if strings.ContainsAny(s, "/\\.*?$") {
		return false
	}
	return regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(s)
}

func (l *Lookup) defs(ctx context.Context, base string) map[string]string {
	l.mu.Lock()
	if d, ok := l.cache[base]; ok {
		l.mu.Unlock()
		return d
	}
	l.mu.Unlock()

	d := fetch(ctx, base)

	l.mu.Lock()
	l.cache[base] = d
	l.mu.Unlock()
	return d
}

// fetch は --help と man の両方を読んで統合する。
//
// 片方だけでは足りないことが実際に多い。たとえば GNU find の --help は
// 要約しか出さず -name は man にしかない。逆に man を持たないツールも多いので、
// 最初に成功した情報源で打ち切らずに全部を混ぜる。先に見つけた定義を優先する。
func fetch(ctx context.Context, base string) map[string]string {
	fields := strings.Fields(base)
	if len(fields) == 0 {
		return nil
	}
	// PATH に無いコマンドは実行しない。
	if _, err := exec.LookPath(fields[0]); err != nil {
		return nil
	}

	merged := map[string]string{}
	absorb := func(d map[string]string) {
		for k, v := range d {
			if _, ok := merged[k]; !ok {
				merged[k] = v
			}
		}
	}

	for _, helpFlag := range []string{"--help", "-h"} {
		argv := append(append([]string{}, fields[1:]...), helpFlag)
		if out := runCapture(ctx, fields[0], argv); out != "" {
			absorb(parseHelp(out))
		}
		if len(merged) > 0 {
			break // --help が効いたなら -h は同じものなので省く
		}
	}

	// man は Unix のみ。サブコマンドは man git-merge の形を試す。
	if out := runCapture(ctx, "man", []string{"--pager=cat", strings.Join(fields, "-")}); out != "" {
		absorb(parseHelp(out))
	}
	return merged
}

func runCapture(ctx context.Context, name string, args []string) string {
	if _, err := exec.LookPath(name); err != nil {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Stdin = nil
	cmd.Env = append(cmd.Environ(), "MANPAGER=cat", "PAGER=cat", "COLUMNS=200", "NO_COLOR=1")
	out, _ := cmd.CombinedOutput() // --help は非0で返す実装も多いので終了コードは見ない
	if len(out) > 400_000 {
		out = out[:400_000]
	}
	return string(out)
}

// parseHelp はヘルプ本文から「フラグ -> 説明」を抜き出す。
//
//	  -n, --dry-run         do not actually do it
//	      --no-ff           create a merge commit even when fast-forward
//	  -f, --force
//	                        long description on the next line
func parseHelp(help string) map[string]string {
	defs := map[string]string{}
	lines := strings.Split(strings.ReplaceAll(help, "\t", "    "), "\n")

	for i, ln := range lines {
		m := flagLine.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		names := flagToken.FindAllString(m[1], -1)
		if len(names) == 0 {
			continue
		}
		// フラグ表記の直後、2スペース以上あけた残りが説明。
		rest := strings.TrimRight(ln[len(m[0]):], " ")
		desc := ""
		if idx := strings.Index(rest, "  "); idx >= 0 {
			desc = strings.TrimSpace(rest[idx:])
		} else if strings.TrimSpace(rest) == "" {
			desc = ""
		}
		// 同じ行に説明が無ければ次行を見る (インデントが深い行のみ採用)。
		if desc == "" && i+1 < len(lines) {
			nxt := lines[i+1]
			if indentOf(nxt) > indentOf(ln) && flagLine.FindStringSubmatch(nxt) == nil {
				desc = strings.TrimSpace(nxt)
			}
		}
		desc = cleanDesc(desc)
		if desc == "" {
			continue
		}
		for _, n := range names {
			if _, exists := defs[n]; !exists {
				defs[n] = desc
			}
		}
	}
	return defs
}

func indentOf(s string) int {
	n := 0
	for _, r := range s {
		if r != ' ' {
			break
		}
		n++
	}
	return n
}

var backspaceBold = regexp.MustCompile(`.\x08`)

func cleanDesc(s string) string {
	s = backspaceBold.ReplaceAllString(s, "") // man の太字表現を落とす
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
