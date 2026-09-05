package editor

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/gimenorum/cmdmuse/internal/core"
)

const promptText = "cmd> "

var (
	colAccent = lipgloss.AdaptiveColor{Light: "#0B6E4F", Dark: "#4ADE80"}
	colDim    = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#8B949E"}
	colFaint  = lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#5A6472"}
	colWarn   = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	colErr    = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	colText   = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#E6EDF3"}

	stDim    = lipgloss.NewStyle().Foreground(colDim)
	stFaint  = lipgloss.NewStyle().Foreground(colFaint)
	stSel    = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	stCmd    = lipgloss.NewStyle().Foreground(colText)
	stWarn   = lipgloss.NewStyle().Foreground(colWarn)
	stErrTxt = lipgloss.NewStyle().Foreground(colErr)
	stOK     = lipgloss.NewStyle().Foreground(colAccent)
	stFlag   = lipgloss.NewStyle().Foreground(colAccent)

	// 提案の2状態。薄い方は未確定、濃い方は Tab で仮確定したもの。
	stGhost     = lipgloss.NewStyle().Foreground(colFaint)
	stGhostHeld = lipgloss.NewStyle().Foreground(colText).Bold(true)
)

func (m Model) View() string {
	w := m.width
	if w < 40 {
		w = 40
	}
	if w > 120 {
		w = 120
	}

	var b strings.Builder
	b.WriteString(m.input.View())
	// 提案はカーソルの先に続けて描く。薄い色は未確定、白は Tab で仮確定した状態。
	if m.ghost != "" {
		if m.ghostHeld {
			b.WriteString(stGhostHeld.Render(m.ghost))
		} else {
			b.WriteString(stGhost.Render(m.ghost))
		}
	}

	if m.err != nil {
		b.WriteString("\n" + stErrTxt.Render("  ✗ "+m.err.Error()))
	}
	if m.loading {
		b.WriteString("\n" + stDim.Render("  … "+m.loadLabel))
	}
	if len(m.comps) > 0 {
		b.WriteString("\n" + m.viewCompletions(w))
	}
	if len(m.cands) > 0 {
		b.WriteString("\n" + m.viewCandidates(w))
		b.WriteString("\n" + m.viewDetail(w))
	}
	if m.asking {
		b.WriteString("\n" + m.ask.View())
	}
	if len(m.cands) > 0 || m.asking || m.ghostHeld {
		b.WriteString("\n" + m.viewHelp())
	}
	return b.String()
}

func (m Model) viewCandidates(w int) string {
	var rows []string
	for i, c := range m.cands {
		marker := "   "
		style := stCmd
		if i == m.cur {
			marker = " ▸ "
			style = stSel
		}

		badge := " "
		switch c.State() {
		case core.ProbeOK:
			badge = stOK.Render("✓")
		case core.ProbeFail:
			badge = stWarn.Render("⚠")
		case core.ProbePending:
			badge = stDim.Render("·")
		}

		tail := ""
		if c.Axis != "" {
			tail += stDim.Render("  " + c.Axis)
		}
		switch c.Risk {
		case core.RiskDestructive:
			tail += stErrTxt.Render("  [破壊的]")
		case core.RiskWrites:
			tail += stWarn.Render("  [書き込み]")
		}

		rows = append(rows, fmt.Sprintf("%s%s%d %s%s",
			marker, badge, i+1, style.Render(fit(c.Command, w-32)), tail))
	}
	head := stFaint.Render(fmt.Sprintf("  候補 %d/%d", m.cur+1, len(m.cands)))
	if m.probing {
		head += stDim.Render("  検証中…")
	}
	return head + "\n" + strings.Join(rows, "\n")
}

func (m Model) viewDetail(w int) string {
	c := m.cands[m.cur]
	var b strings.Builder

	for _, p := range c.FailedPreconds() {
		line := stWarn.Render("  ⚠ " + p.Desc)
		if p.Output != "" {
			line += stDim.Render(" — " + p.Output)
		}
		b.WriteString(line + "\n")
	}
	for _, f := range c.Flags {
		def := f.Def
		if def == "" {
			def = stDim.Render("(定義を取得できませんでした)")
		} else {
			def = stDim.Render(fit(def, w-20))
		}
		b.WriteString("  " + stFlag.Render(f.Flag) + "  " + def + "\n")
	}
	if c.Explanation != "" {
		b.WriteString(stCmd.Width(w - 4).Render("  " + c.Explanation))
	} else if m.explaining {
		b.WriteString(stDim.Render("  解説を生成中…"))
	}
	return strings.TrimRight(b.String(), "\n")
}

// viewCompletions は Tab で決まりきらなかった候補を並べる。
// 端末を埋め尽くさないよう件数を絞り、残りは数だけ伝える。
func (m Model) viewCompletions(w int) string {
	const maxShown = 24
	shown := m.comps
	rest := 0
	if len(shown) > maxShown {
		rest = len(shown) - maxShown
		shown = shown[:maxShown]
	}

	colW := 0
	for _, c := range shown {
		if n := lipgloss.Width(c); n > colW {
			colW = n
		}
	}
	colW += 2
	cols := max(1, (w-2)/colW)

	var b strings.Builder
	for i, c := range shown {
		if i > 0 && i%cols == 0 {
			b.WriteString("\n")
		}
		if i%cols == 0 {
			b.WriteString("  ")
		}
		b.WriteString(stDim.Render(c + strings.Repeat(" ", colW-lipgloss.Width(c))))
	}
	if rest > 0 {
		b.WriteString(stFaint.Render(fmt.Sprintf("\n  ...他 %d 件", rest)))
	}
	return strings.TrimRight(b.String(), " ")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m Model) viewHelp() string {
	if m.asking {
		return stFaint.Render("  Enter 質問   Esc 取消")
	}
	if m.ghostHeld {
		return stFaint.Render("  Enter 取り込む   Esc 取消")
	}
	return stFaint.Render("  Tab 候補   1-9 選択   Ctrl+O 深堀り   Enter 実行   Esc 元に戻す")
}

func fit(s string, w int) string {
	if w < 10 {
		w = 10
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)+"…") > w {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}
