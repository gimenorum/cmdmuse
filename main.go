// cmdmuse は行の中に書いた AI(...) を実コマンドに置き換えるシェルフロントエンド。
//
//	cmd> cat server.log | AI(直近のエラーだけ抜き出したい) | less
//	     ↓ 手が止まって5秒
//	cmd> cat server.log | grep -i error | tail -20 | less
//
// 候補はアプローチの異なるものが並び、Tab で切り替えると行がその場で差し替わる。
// 各候補は実行前に前提条件を副作用のないコマンドで検証し、フラグの意味は
// --help / man の実定義だけを根拠に解説する。
// 確定した行はユーザーのシェルにそのまま渡して実行する。
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/gimenorum/cmdmuse/internal/core"
	"github.com/gimenorum/cmdmuse/internal/editor"
	"github.com/gimenorum/cmdmuse/internal/llm"
	"github.com/gimenorum/cmdmuse/internal/session"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help":
			usage()
			return
		}
	}

	client := llm.New()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := client.Probe(ctx)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "LLM に接続できません: %v\n", err)
		fmt.Fprintf(os.Stderr, "CMDMUSE_BASE_URL で接続先を指定できます (既定: %s)\n", client.BaseURL)
		os.Exit(1)
	}

	sctx, scancel := context.WithTimeout(context.Background(), 8*time.Second)
	sess := session.Capture(sctx)
	scancel()

	hist := editor.LoadHistory()
	defer hist.Save()

	fmt.Printf("cmdmuse — 行の中に AI(やりたいこと) と書くと %.0f秒後に展開します。exit / Ctrl+D で終了。\n",
		editor.IdleDelay.Seconds())

	for {
		sess.Cwd, _ = os.Getwd()
		sess.Recent = hist.Recent(20)

		res, err := readLine(client, sess, hist)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return
		}
		if res.Quit {
			return
		}
		if res.Interrupt {
			continue
		}
		if !res.Submit || strings.TrimSpace(res.Line) == "" {
			continue
		}

		line := strings.TrimSpace(res.Line)
		hist.Add(line)
		if line == "exit" || line == "quit" {
			return
		}
		if cd, ok := parseCd(line); ok {
			// cd は子プロセスでは意味が無いので自分で移動する。
			if err := os.Chdir(cd); err != nil {
				fmt.Fprintf(os.Stderr, "cd: %v\n", err)
			}
			continue
		}
		runInShell(line)
	}
}

func readLine(c *llm.Client, sess core.SessionContext, h *editor.History) (editor.Result, error) {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		w = 100
	}
	m := editor.New(c, sess, h, w)
	// 代替スクリーンには入らない。実行結果は端末にそのまま流れて残る。
	p := tea.NewProgram(m)
	final, err := p.Run()
	if err != nil {
		return editor.Result{}, err
	}
	fm, ok := final.(editor.Model)
	if !ok {
		return editor.Result{}, fmt.Errorf("内部エラー: モデルの型が違う")
	}
	return fm.Result, nil
}

// parseCd は `cd` と `cd <dir>` だけを拾う。&& や | が付いていたらシェルに任せる。
func parseCd(line string) (string, bool) {
	if strings.ContainsAny(line, "|&;") {
		return "", false
	}
	f := strings.Fields(line)
	if len(f) == 0 || f[0] != "cd" {
		return "", false
	}
	if len(f) == 1 {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		return home, true
	}
	if len(f) > 2 {
		return "", false
	}
	dir := f[1]
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = home + dir[1:]
		}
	}
	return dir, true
}

// runInShell は確定した行をユーザーのシェルで実行する。
// 端末をそのまま渡すので vim や less のような対話的なコマンドも動く。
func runInShell(line string) {
	name, args := shellFor(line)
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !asExitError(err, &ee) {
			fmt.Fprintf(os.Stderr, "cmdmuse: %v\n", err)
		}
	}
}

func asExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}

func shellFor(line string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "powershell.exe", []string{"-NoProfile", "-Command", line}
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	return sh, []string{"-c", line}
}

func usage() {
	fmt.Printf(`cmdmuse - 行の中の AI(...) を実コマンドに置き換えるシェルフロントエンド

  cmdmuse            起動する

使い方:
  cmd> cat server.log | AI(直近のエラーだけ抜き出したい) | less
       手が止まって%.0f秒で AI(...) の部分だけが実コマンドに置き換わる

環境変数:
  CMDMUSE_BASE_URL   OpenAI互換エンドポイント (既定: http://127.0.0.1:18080/v1)
  CMDMUSE_API_KEY    APIキー (不要なら未設定でよい)
  CMDMUSE_MODEL      モデル名 (未指定なら /models の先頭)

キー:
  Tab                候補があれば切り替え、無ければコマンド名・パスを補完
  ↑↓                 候補を切り替える
  1-9                候補を直接選ぶ
  Ctrl+O             選択中の候補について追加で質問する
  Enter              行を実行する
  Esc                展開を取り消して AI(...) に戻す
  ↑↓                 (候補が無いとき) 履歴をたどる
  Ctrl+D, exit       終了

確定した行は $SHELL で実行される。候補は自動では実行されない。
`, editor.IdleDelay.Seconds())
}
