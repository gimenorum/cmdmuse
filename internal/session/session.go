// Package session は直前のシェル操作を文脈として集める。
//
// 取得は起動時に一度だけ行う。TUI が代替スクリーンに入ったあとでは
// tmux capture-pane が自分自身の画面を写してしまうため、
// 「起動した時点で画面に出ていたもの」を文脈とするのが正しい意味になる。
package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gimenorum/cmdmuse/internal/core"
)

const maxLines = 60

// Capture は利用可能な最良の手段でセッション文脈を集める。
func Capture(ctx context.Context) core.SessionContext {
	sc := core.SessionContext{
		OS:    osLabel(),
		Shell: shellName(),
	}
	if cwd, err := os.Getwd(); err == nil {
		sc.Cwd = cwd
	}

	if lines := fromTmux(ctx); len(lines) > 0 {
		sc.Source, sc.Recent = "tmux", lines
		return sc
	}
	if lines := fromPowerShellHistory(ctx); len(lines) > 0 {
		sc.Source, sc.Recent = "pwsh-history", lines
		return sc
	}
	if lines := fromShellHistory(); len(lines) > 0 {
		sc.Source, sc.Recent = "history", lines
		return sc
	}
	sc.Source = "none"
	return sc
}

// fromTmux は画面の入出力をそのまま取る。唯一「出力まで」読める経路。
func fromTmux(ctx context.Context) []string {
	if os.Getenv("TMUX") == "" {
		return nil
	}
	out := run(ctx, "tmux", "capture-pane", "-p", "-S", "-120")
	if out == "" {
		return nil
	}
	return trimTail(strings.Split(out, "\n"))
}

// fromPowerShellHistory は PSReadLine の履歴を読む。入力のみで出力は取れない。
func fromPowerShellHistory(ctx context.Context) []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	shell := "powershell"
	if _, err := exec.LookPath("pwsh"); err == nil {
		shell = "pwsh"
	}
	out := run(ctx, shell, "-NoProfile", "-Command",
		"Get-Content (Get-PSReadLineOption).HistorySavePath -Tail 30 -ErrorAction SilentlyContinue")
	if out == "" {
		return nil
	}
	return trimTail(strings.Split(out, "\n"))
}

// fromShellHistory は履歴ファイルを読む。bash は終了時にしか書かないので
// 現在のセッションの内容が入っていないことがある。最後の手段。
func fromShellHistory() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var path string
	switch {
	case strings.Contains(shellName(), "zsh"):
		path = filepath.Join(home, ".zsh_history")
	default:
		path = filepath.Join(home, ".bash_history")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) > 30 {
		lines = lines[len(lines)-30:]
	}
	// zsh の拡張履歴形式 (: 1700000000:0;cmd) からコマンド部分を取る。
	for i, ln := range lines {
		if strings.HasPrefix(ln, ": ") {
			if j := strings.Index(ln, ";"); j >= 0 {
				lines[i] = ln[j+1:]
			}
		}
	}
	return trimTail(lines)
}

func run(ctx context.Context, name string, args ...string) string {
	if _, err := exec.LookPath(name); err != nil {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// trimTail は末尾の空行を落とし、直近 maxLines 行に絞る。
func trimTail(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimRight(ln, " \t\r")
		if len(ln) > 400 {
			ln = ln[:400] + "…"
		}
		out = append(out, ln)
	}
	return out
}

func shellName() string {
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	if s := os.Getenv("SHELL"); s != "" {
		return filepath.Base(s)
	}
	return ""
}

func osLabel() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "linux":
		if b, err := os.ReadFile("/proc/version"); err == nil &&
			strings.Contains(strings.ToLower(string(b)), "microsoft") {
			return "Linux (WSL2)"
		}
		return "Linux"
	}
	return runtime.GOOS
}
