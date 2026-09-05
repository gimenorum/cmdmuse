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
		if m.compOn {
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
	if len(m.cands) > 0 || m.asking || m.compOn {
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

// viewCompletions は候補をグリッドに並べ、選択中を強調する。
//
// 端末を埋め尽くさないよう表示は maxRows 行までとし、選択が外に出たら
// 行単位でスクロールする。列をずらすと候補の位置が毎回動いて追えなくなるため、
// 切り出しは必ず行の頭で行う。
func (m Model) viewCompletions(w int) string {
	const maxRows = 8

	cols := gridCols(m.comps, w)
	colW := gridColWidth(m.comps)
	total := len(m.comps)
	totalRows := (total + cols - 1) / cols

	// 選択中の行が入るように、表示する行の範囲を決める。
	curRow := m.compCur / cols
	firstRow := 0
	if curRow >= maxRows {
		firstRow = curRow - maxRows + 1
	}
	if firstRow > totalRows-maxRows {
		firstRow = totalRows - maxRows
	}
	if firstRow < 0 {
		firstRow = 0
	}

	from := firstRow * cols
	to := from + maxRows*cols
	if to > total {
		to = total
	}

	var b strings.Builder
	for i := from; i < to; i++ {
		if i > from && (i-from)%cols == 0 {
			b.WriteString("\n")
		}
		if (i-from)%cols == 0 {
			b.WriteString(" ")
		}
		mark, style := "  ", stDim
		if i == m.compCur {
			mark, style = "▸ ", stSel
		}
		pad := colW - lipgloss.Width(m.comps[i]) - 2
		if pad < 1 {
			pad = 1
		}
		b.WriteString(mark + style.Render(m.comps[i]) + strings.Repeat(" ", pad))
	}

	if hidden := total - (to - from); hidden > 0 {
		b.WriteString(stFaint.Render(fmt.Sprintf("\n  ...他 %d 件", hidden)))
	}
	return strings.TrimRight(b.String(), " ")
}

// gridColWidth と gridCols は一覧の並びを決める。
// 矢印キーで1行ぶん動くために、モデル側からも同じ計算を使う。
func gridColWidth(items []string) int {
	w := 0
	for _, c := range items {
		if n := lipgloss.Width(c); n > w {
			w = n
		}
	}
	return w + 3
}

func gridCols(items []string, width int) int {
	return max(1, (width-2)/gridColWidth(items))
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
	if m.compOn {
		return stFaint.Render(fmt.Sprintf("  %d/%d   ←→/Tab 選択   ↑↓ 行移動   Enter 取り込む   Esc 取消",
			m.compCur+1, len(m.comps)))
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
