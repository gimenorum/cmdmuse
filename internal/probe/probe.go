// Package probe は候補の前提条件を検証する。
//
// probe 文字列は LLM が生成したものであり信頼できない。したがって
//   - シェルを一切介さない (exec.Command に argv を直接渡す)
//   - コマンド名を許可リストで絞る
//   - サブコマンドを持つものは第2トークンも絞る
//   - 破壊的になりうるフラグ (find -delete 等) を個別に弾く
//
// の4段構えで防御する。許可リストに無いものは実行せず Skip 扱いにする。
package probe

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gimenorum/cmdmuse/internal/core"
)

// 読み取り専用として実行を許すコマンド。値が nil なら任意の引数を許す。
// 値がある場合は第1引数 (サブコマンド) がその集合に含まれる場合のみ許す。
var allowed = map[string]map[string]bool{
	"ls":       nil,
	"stat":     nil,
	"file":     nil,
	"readlink": nil,
	"realpath": nil,
	"dirname":  nil,
	"basename": nil,
	"cat":      nil,
	"head":     nil,
	"tail":     nil,
	"wc":       nil,
	"grep":     nil,
	"find":     nil, // フラグ側で個別に弾く
	"uname":    nil,
	"hostname": nil,
	"whoami":   nil,
	"id":       nil,
	"pwd":      nil,
	"date":     nil,
	"printenv": nil,
	"locale":   nil,
	"tty":      nil,
	"df":       nil,
	"du":       nil,

	"git": {
		"rev-parse": true, "branch": true, "status": true, "log": true,
		"show": true, "remote": true, "ls-files": true, "ls-remote": true,
		"diff": true, "config": true, "describe": true, "tag": true,
		"cat-file": true, "symbolic-ref": true, "check-ignore": true,
		"stash": true, // stash list のみ。下で追加検査する
	},
	"docker": {"ps": true, "images": true, "version": true, "info": true},
	"kubectl": {"get": true, "version": true, "config": true,
		"cluster-info": true, "api-resources": true},
	"systemctl": {"status": true, "is-active": true, "is-enabled": true,
		"list-units": true, "show": true},
	"npm":    {"ls": true, "list": true, "view": true, "config": true, "root": true},
	"pip":    {"show": true, "list": true, "freeze": true},
	"pip3":   {"show": true, "list": true, "freeze": true},
	"go":     {"version": true, "env": true, "list": true},
	"cargo":  {"--version": true, "version": true, "tree": true},
	"python": {"--version": true, "-V": true},
	"node":   {"--version": true, "-v": true},
	"java":   {"--version": true, "-version": true},
}

// find に渡ってはいけないフラグ。副作用を持つ。
var findForbidden = map[string]bool{
	"-delete": true, "-exec": true, "-execdir": true,
	"-ok": true, "-okdir": true, "-fls": true, "-fprint": true,
	"-fprint0": true, "-fprintf": true,
}

// git のサブコマンドごとに、さらに絞りたいもの。
var gitSubSub = map[string]map[string]bool{
	"stash": {"list": true, "show": true},
}

// Runner は前提条件を検証する。
type Runner struct {
	Timeout time.Duration
}

func NewRunner() *Runner { return &Runner{Timeout: 4 * time.Second} }

// RunAll は候補群の前提条件を並列に検証し、結果を書き戻す。
func (r *Runner) RunAll(ctx context.Context, cands []core.Candidate) {
	var wg sync.WaitGroup
	for i := range cands {
		for j := range cands[i].Preconds {
			wg.Add(1)
			go func(p *core.Precond) {
				defer wg.Done()
				p.State, p.Output = r.run(ctx, p.Probe)
			}(&cands[i].Preconds[j])
		}
	}
	wg.Wait()
}

func (r *Runner) run(ctx context.Context, probe string) (core.ProbeState, string) {
	probe = strings.TrimSpace(probe)
	if probe == "" {
		return core.ProbeSkip, ""
	}
	if hasShellMeta(probe) {
		return core.ProbeSkip, "シェル演算子を含むため検証しません"
	}
	argv, err := tokenize(probe)
	if err != nil || len(argv) == 0 {
		return core.ProbeSkip, "解釈できません"
	}
	argv = rewriteProbe(argv)

	// シェル組み込みや存在確認は外部プロセスを起こさず自前で解決する。
	// Windows でも同じ判定になる。
	if st, out, handled := builtinProbe(argv); handled {
		return st, out
	}

	if !permitted(argv) {
		return core.ProbeSkip, "許可されていないコマンドのため検証しません"
	}

	// シェルを通さないので glob は自分で展開する。展開しないと
	// ls *.txt が常に失敗し、ファイルがあっても「無い」と誤判定する。
	argv, matched := expandGlobs(argv)
	if !matched {
		return core.ProbeFail, "該当するファイルがありません"
	}

	cctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if cctx.Err() == context.DeadlineExceeded {
		return core.ProbeSkip, "タイムアウト"
	}
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return core.ProbeFail, firstLine(text)
	}
	// git branch --list のように、成功しても出力が空なら「無い」を意味する場合がある。
	if isEmptyMeansAbsent(argv) && text == "" {
		return core.ProbeFail, "該当なし"
	}
	return core.ProbeOK, firstLine(text)
}

// rewriteProbe は「意味は正しいが判定を間違える probe」を等価な正しい形に直す。
//
// 代表例が .git の存在確認。git はリポジトリのルートを見つけるまで親を遡るので、
// サブディレクトリにいても add や merge は通る。ところが test -d .git は
// カレントしか見ないため、正常なリポジトリで全候補を NG にしてしまう。
// LLM がどちらの書き方を選ぶかは制御できないので、ここで吸収する。
func rewriteProbe(argv []string) []string {
	if len(argv) < 2 {
		return argv
	}
	last := argv[len(argv)-1]
	if !isGitDirPath(last) {
		return argv
	}
	switch argv[0] {
	case "test", "[", "ls", "stat", "file", "readlink", "realpath":
		return []string{"git", "rev-parse", "--git-dir"}
	}
	return argv
}

func isGitDirPath(s string) bool {
	s = strings.TrimSuffix(s, "/")
	switch s {
	case ".git", "./.git":
		return true
	}
	return false
}

// builtinProbe は command -v / which / type / test を自前で処理する。
func builtinProbe(argv []string) (core.ProbeState, string, bool) {
	switch argv[0] {
	case "command", "type":
		// command -v X / type X
		var target string
		for _, a := range argv[1:] {
			if !strings.HasPrefix(a, "-") {
				target = a
				break
			}
		}
		if target == "" {
			return core.ProbeSkip, "", true
		}
		if p, err := exec.LookPath(target); err == nil {
			return core.ProbeOK, p, true
		}
		return core.ProbeFail, target + " が PATH にありません", true

	case "which":
		if len(argv) < 2 {
			return core.ProbeSkip, "", true
		}
		if p, err := exec.LookPath(argv[1]); err == nil {
			return core.ProbeOK, p, true
		}
		return core.ProbeFail, argv[1] + " が PATH にありません", true

	case "test", "[":
		return testProbe(argv)
	}
	return 0, "", false
}

// testProbe は test -f/-d/... を自前で判定する。
// 画面には理由が出るので、状態だけでなく必ず説明文も返す。
func testProbe(argv []string) (core.ProbeState, string, bool) {
	args := argv[1:]
	if len(args) > 0 && args[len(args)-1] == "]" {
		args = args[:len(args)-1]
	}
	if len(args) != 2 {
		return core.ProbeSkip, "解釈できません", true
	}
	op, path := args[0], args[1]
	switch op {
	case "-e", "-f", "-d", "-s", "-r", "-w", "-x":
	default:
		return core.ProbeSkip, "未対応の判定 " + op, true
	}

	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return core.ProbeFail, path + " がありません", true
		}
		return core.ProbeFail, firstLine(err.Error()), true
	}
	switch op {
	case "-f":
		if !fi.Mode().IsRegular() {
			return core.ProbeFail, path + " は通常ファイルではありません", true
		}
	case "-d":
		if !fi.IsDir() {
			return core.ProbeFail, path + " はディレクトリではありません", true
		}
	case "-s":
		if fi.Size() == 0 {
			return core.ProbeFail, path + " は空です", true
		}
	}
	abs, aerr := filepath.Abs(path)
	if aerr != nil {
		abs = path
	}
	return core.ProbeOK, abs, true
}

func permitted(argv []string) bool {
	subs, ok := allowed[argv[0]]
	if !ok {
		return false
	}
	if argv[0] == "find" {
		for _, a := range argv[1:] {
			if findForbidden[a] {
				return false
			}
		}
	}
	if subs == nil {
		return true
	}
	if len(argv) < 2 {
		return false
	}
	sub := argv[1]
	if !subs[sub] {
		return false
	}
	if ss, ok := gitSubSub[sub]; ok && argv[0] == "git" {
		if len(argv) < 3 || !ss[argv[2]] {
			return false
		}
	}
	// git config は読み取りだけ許す (--get / --list)。
	if argv[0] == "git" && sub == "config" {
		read := false
		for _, a := range argv[2:] {
			if a == "--get" || a == "--list" || a == "-l" || a == "--get-all" {
				read = true
			}
		}
		return read
	}
	return true
}

// isEmptyMeansAbsent は「終了コード0だが出力が空 = 対象が存在しない」コマンドか。
func isEmptyMeansAbsent(argv []string) bool {
	if argv[0] != "git" || len(argv) < 2 {
		return false
	}
	switch argv[1] {
	case "branch", "tag", "ls-files", "check-ignore":
		return true
	}
	return false
}

// expandGlobs は引数中の glob を展開する。
// 1つでも「パターンなのに何にも一致しない」ものがあれば matched=false を返す。
// find -name '*.txt' のようにパターンを値として渡す場合は展開してはいけないので、
// 直前の引数がパターンを取るフラグのときは素通しする。
func expandGlobs(argv []string) (out []string, matched bool) {
	out = append(out, argv[0])
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if !strings.ContainsAny(a, "*?[") || literalPattern(argv, i) {
			out = append(out, a)
			continue
		}
		hits, err := filepath.Glob(a)
		if err != nil || len(hits) == 0 {
			return nil, false
		}
		out = append(out, hits...)
	}
	return out, true
}

// literalPattern は argv[i] が「展開せず文字列として渡す値」かを判定する。
func literalPattern(argv []string, i int) bool {
	if i == 0 {
		return false
	}
	switch argv[i-1] {
	case "-name", "-iname", "-path", "-ipath", "-wholename", "-regex",
		"-e", "--include", "--exclude", "--glob":
		return true
	}
	// grep のパターンは第1引数。
	if argv[0] == "grep" && i == 1 {
		return true
	}
	return false
}

func hasShellMeta(s string) bool {
	return strings.ContainsAny(s, "|&;<>`$()\n\r{}")
}

// tokenize は引用符だけを解釈する簡易分割。展開は一切しない。
func tokenize(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	var quote rune
	started := false

	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t':
			if started {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("引用符が閉じていません")
	}
	if started {
		out = append(out, cur.String())
	}
	return out, nil
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
