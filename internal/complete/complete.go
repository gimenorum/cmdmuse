// Package complete は行編集の Tab 補完を提供する。
//
// bash の compgen には委譲しない。委譲するとユーザーの入力をシェルに
// 通すことになり、このツールが probe で避けているのと同じ危険を
// 行編集側で作ってしまう。Windows で動かないという問題もある。
// PATH の走査と os.ReadDir だけで足りるので、ここで完結させる。
package complete

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Result は補完の結果。
type Result struct {
	// Candidates は候補。1件なら確定、複数なら共通接頭辞まで進める。
	Candidates []string
	// Start は行のうち置き換える範囲の開始位置 (バイト)。
	Start int
	// End は置き換える範囲の終端 (バイト)。カーソル位置と同じ。
	End int
}

// Complete は line の pos 位置にあるトークンを補完する。
func Complete(line string, pos int) Result {
	if pos > len(line) {
		pos = len(line)
	}
	start := wordStart(line, pos)
	word := line[start:pos]

	var cands []string
	if isCommandPosition(line, start) && !strings.ContainsAny(word, "/\\~.") {
		cands = commands(word)
	} else {
		cands = paths(word)
	}
	sort.Strings(cands)
	return Result{Candidates: cands, Start: start, End: pos}
}

// wordStart は pos の直前にあるトークンの開始位置を返す。
// 引用符やエスケープされた空白はトークンを切らない。
func wordStart(line string, pos int) int {
	i := pos
	for i > 0 {
		c := line[i-1]
		if c == ' ' || c == '\t' {
			// バックスラッシュでエスケープされた空白は区切りではない。
			if i >= 2 && line[i-2] == '\\' {
				i -= 2
				continue
			}
			break
		}
		if c == '|' || c == ';' || c == '&' || c == '(' || c == ')' ||
			c == '<' || c == '>' || c == '"' || c == '\'' {
			break
		}
		i--
	}
	return i
}

// isCommandPosition は start がコマンド名の位置かを見る。
// 行頭、またはパイプ・連結演算子の直後ならコマンド名。
func isCommandPosition(line string, start int) bool {
	for i := start - 1; i >= 0; i-- {
		switch line[i] {
		case ' ', '\t':
			continue
		case '|', ';', '&', '(':
			return true
		default:
			return false
		}
	}
	return true
}

// pathCache は PATH 上の実行可能ファイル名。
// 打鍵ごとに提案を出すので、PATH 全体の走査は一度きりにする。
var pathCache struct {
	once  sync.Once
	names []string
}

// InvalidatePathCache は PATH の走査結果を捨てる。
// セッション中にコマンドを入れた場合に呼ぶ。
func InvalidatePathCache() {
	pathCache.once = sync.Once{}
	pathCache.names = nil
}

// WarmPathCache は PATH の走査を先に済ませておく。
//
// WSL では PATH に Windows 側 (/mnt/c/...) が数十個入っていて、
// 9p 越しの ReadDir に実測 5.5 秒かかる。打鍵の途中でこれを踏むと
// その間の入力が固まって溜まり、終わった瞬間に一気に流れ込む。
// 起動直後に別 goroutine で走らせて、最初の打鍵までに終わらせる。
func WarmPathCache() {
	go executables()
}

func executables() []string {
	pathCache.once.Do(func() {
		seen := map[string]bool{}
		for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
			if dir == "" {
				continue
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				name := e.Name()
				if seen[name] || e.IsDir() || !executable(dir, e) {
					continue
				}
				seen[name] = true
				pathCache.names = append(pathCache.names, name)
			}
		}
		sort.Strings(pathCache.names)
	})
	return pathCache.names
}

// commands は PATH 上の実行可能ファイルとシェル組み込みから候補を集める。
func commands(prefix string) []string {
	seen := map[string]bool{}
	var out []string

	for _, b := range builtins {
		if strings.HasPrefix(b, prefix) && !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	for _, name := range executables() {
		if strings.HasPrefix(name, prefix) && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func executable(dir string, e os.DirEntry) bool {
	if runtime.GOOS == "windows" {
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".exe", ".bat", ".cmd", ".com", ".ps1":
			return true
		}
		return false
	}
	info, err := e.Info()
	if err != nil {
		return false
	}
	// シンボリックリンクは実体を見る。PATH には多い。
	if info.Mode()&os.ModeSymlink != 0 {
		if st, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
			info = st
		} else {
			return false
		}
	}
	return info.Mode()&0o111 != 0
}

// paths はファイル・ディレクトリ名を補完する。
// ディレクトリには / を付けて、続けて打てるようにする。
func paths(word string) []string {
	expanded, tildePrefix := expandTilde(word)

	dir, base := filepath.Split(expanded)
	lookIn := dir
	if lookIn == "" {
		lookIn = "."
	}

	entries, err := os.ReadDir(lookIn)
	if err != nil {
		return nil
	}

	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base) {
			continue
		}
		// 明示的に . を打っていないなら隠しファイルは出さない。
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		full := dir + name
		if e.IsDir() {
			full += "/"
		}
		// ~ で始まっていたなら ~ のまま返す。展開後の絶対パスに化けさせない。
		if tildePrefix != "" {
			full = tildePrefix + strings.TrimPrefix(full, expandedHome())
		}
		out = append(out, escapeSpaces(full))
	}
	return out
}

// expandTilde は先頭の ~ をホームディレクトリに置き換える。
// 置き換えた場合は元の接頭辞 ("~" または "~/") を返す。
func expandTilde(word string) (expanded, tildePrefix string) {
	if word != "~" && !strings.HasPrefix(word, "~/") {
		return word, ""
	}
	home := expandedHome()
	if home == "" {
		return word, ""
	}
	if word == "~" {
		return home + "/", "~"
	}
	return home + word[1:], "~"
}

func expandedHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// escapeSpaces は空白を含むパスをそのまま行に差し込めるようにする。
func escapeSpaces(s string) string {
	if !strings.ContainsAny(s, " \t") {
		return s
	}
	return strings.NewReplacer(" ", `\ `, "\t", `\	`).Replace(s)
}

// CommonPrefix は候補群の共通接頭辞を返す。
// 候補が複数あるとき、確定できるところまでは進めるために使う。
func CommonPrefix(cands []string) string {
	if len(cands) == 0 {
		return ""
	}
	p := cands[0]
	for _, c := range cands[1:] {
		for !strings.HasPrefix(c, p) {
			p = p[:len(p)-1]
			if p == "" {
				return ""
			}
		}
	}
	return p
}

// builtins はシェル組み込み。PATH には無いが打てるもの。
var builtins = []string{
	"cd", "exit", "quit", "echo", "export", "unset", "alias", "unalias",
	"source", "set", "pwd", "test", "type", "command", "history", "jobs",
	"kill", "wait", "read", "eval", "exec", "trap", "umask", "ulimit",
}
