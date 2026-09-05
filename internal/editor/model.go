// Package editor は cmdmuse の行エディタ。
//
// 普通のシェルのように1行を編集させ、行の中に AI(...) が書かれたまま
// 入力が IdleDelay 秒止まると、その範囲を実コマンドに置き換える。
// 代替スクリーンには入らず、プロンプト行とその下の候補だけを描く。
package editor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/gimenorum/cmdmuse/internal/complete"
	"github.com/gimenorum/cmdmuse/internal/core"
	"github.com/gimenorum/cmdmuse/internal/expand"
	"github.com/gimenorum/cmdmuse/internal/llm"
	"github.com/gimenorum/cmdmuse/internal/probe"
	"github.com/gimenorum/cmdmuse/internal/spec"
)

// IdleDelay は AI(...) を書いた手が止まってから展開するまでの待ち時間。
const IdleDelay = 5 * time.Second

// explainDelay は候補を切り替えてから解説を取りに行くまでの待ち時間。
const explainDelay = 300 * time.Millisecond

// Result は1行ぶんの編集結果。
type Result struct {
	Line      string // 実行する行
	Submit    bool   // Enter で確定したか
	Quit      bool   // Ctrl+D / exit で終了するか
	Interrupt bool   // Ctrl+C で中断したか
}

type Model struct {
	client  *llm.Client
	lookup  *spec.Lookup
	runner  *probe.Runner
	session core.SessionContext
	history *History

	input textinput.Model

	// span は展開対象。cands が空でなければ、この範囲が置き換わっている。
	span  expand.Span
	goal  string
	cands []core.Candidate
	cur   int
	// prefix/suffix は AI(...) の前後。候補を切り替えるたびに組み直す。
	prefix string
	suffix string

	seq        int
	genSeq     int
	loading    bool
	loadLabel  string
	probing    bool
	explaining bool
	err        error

	asking bool // 深堀りの質問を入力中
	ask    textinput.Model

	// comps は Tab 補完で複数候補が出たときの一覧。
	// 一度で決まらなかったことを見せるためだけに持ち、選択はさせない。
	comps []string

	cancel context.CancelFunc
	width  int

	Result Result
}

func New(c *llm.Client, sc core.SessionContext, h *History, width int) Model {
	in := textinput.New()
	in.Prompt = promptText
	in.Placeholder = "コマンド、または AI(やりたいこと)"
	in.CharLimit = 4000
	in.Focus()

	ask := textinput.New()
	ask.Prompt = "  ? "
	ask.Placeholder = "この候補について聞く (例: 失敗したら戻せる?)"
	ask.CharLimit = 300

	if width <= 0 {
		width = 100
	}
	return Model{
		client:  c,
		lookup:  spec.NewLookup(),
		runner:  probe.NewRunner(),
		session: sc,
		history: h,
		input:   in,
		ask:     ask,
		width:   width,
	}
}

func (m Model) Init() tea.Cmd { return textinput.Blink }

// ---- メッセージ ----

type idleMsg struct{ seq int }
type candidatesMsg struct {
	seq   int
	goal  string
	cands []core.Candidate
	err   error
}
type enrichedMsg struct {
	seq   int
	cands []core.Candidate
}
type explainTickMsg struct {
	seq      int
	strategy string
}
type explainMsg struct {
	strategy string
	text     string
	err      error
}

func idleCmd(seq int) tea.Cmd {
	return tea.Tick(IdleDelay, func(time.Time) tea.Msg { return idleMsg{seq} })
}

func explainTickCmd(seq int, strategy string) tea.Cmd {
	return tea.Tick(explainDelay, func(time.Time) tea.Msg {
		return explainTickMsg{seq: seq, strategy: strategy}
	})
}

// ---- 非同期処理 ----
//
// cancel は戻り値で返す。モデルに書き込む形にすると
// `return m, m.generateCmd(...)` で m が先に複製され cancel が失われる。

func (m Model) generateCmd(goal string, seq int) (tea.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	client, sess, lc := m.client, m.session, m.lineContext()
	return func() tea.Msg {
		cands, err := client.Candidates(ctx, goal, sess, lc)
		return candidatesMsg{seq: seq, goal: goal, cands: cands, err: err}
	}, cancel
}

func (m Model) followupCmd(goal string, base core.Candidate, q string, seq int) (tea.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	client, sess, lc := m.client, m.session, m.lineContext()
	return func() tea.Msg {
		cands, err := client.Followup(ctx, goal, base, q, sess, lc)
		return candidatesMsg{seq: seq, goal: goal, cands: cands, err: err}
	}, cancel
}

func (m Model) enrichCmd(cands []core.Candidate, seq int) tea.Cmd {
	lookup, runner := m.lookup, m.runner
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		for i := range cands {
			lookup.Annotate(ctx, &cands[i])
			cands[i].Risk = probe.Classify(cands[i].Command)
		}
		runner.RunAll(ctx, cands)
		return enrichedMsg{seq: seq, cands: cands}
	}
}

func (m Model) explainCmd(cand core.Candidate, others []core.Candidate) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		text, err := client.Explain(ctx, cand, others)
		return explainMsg{strategy: cand.Strategy, text: text, err: err}
	}
}

func (m *Model) startInflight(c context.CancelFunc) {
	if m.cancel != nil {
		m.cancel()
	}
	m.cancel = c
}

func (m *Model) cancelInflight() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

// ---- Update ----

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case idleMsg:
		if msg.seq != m.seq || m.loading || m.asking {
			return m, nil
		}
		return m.tryExpand()

	case candidatesMsg:
		if msg.seq != m.genSeq {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			// 自分で止めたものはエラーではない。
			// Ctrl+C や打鍵で cancel した結果がここに届く。
			if !errors.Is(msg.err, context.Canceled) {
				m.err = msg.err
			}
			return m, nil
		}
		m.err = nil
		m.goal = msg.goal
		m.cands = msg.cands
		m.cur = 0
		m.applyCandidate()
		cmds := []tea.Cmd{m.enrichCmd(m.cands, msg.seq)}
		m.probing = true
		if len(m.cands) > 0 {
			cmds = append(cmds, explainTickCmd(msg.seq, m.cands[0].Strategy))
		}
		return m, tea.Batch(cmds...)

	case enrichedMsg:
		if msg.seq != m.genSeq {
			return m, nil
		}
		m.probing = false
		for i := range msg.cands {
			for j := range m.cands {
				if m.cands[j].Strategy == msg.cands[i].Strategy && m.cands[j].Explained {
					msg.cands[i].Explanation = m.cands[j].Explanation
					msg.cands[i].Explained = true
				}
			}
		}
		m.cands = msg.cands
		return m, nil

	case explainTickMsg:
		if msg.seq != m.genSeq || len(m.cands) == 0 {
			return m, nil
		}
		if m.cands[m.cur].Strategy != msg.strategy || m.cands[m.cur].Explained {
			return m, nil
		}
		m.explaining = true
		return m, m.explainCmd(m.cands[m.cur], m.cands)

	case explainMsg:
		m.explaining = false
		for i := range m.cands {
			if m.cands[i].Strategy == msg.strategy {
				if msg.err != nil {
					m.cands[i].Explanation = "解説を取得できませんでした: " + msg.err.Error()
				} else {
					m.cands[i].Explanation = strings.TrimSpace(msg.text)
				}
				m.cands[i].Explained = true
			}
		}
		return m, nil
	}
	return m, nil
}

// tryExpand は行の中の AI(...) を探し、あれば候補生成を始める。
func (m Model) tryExpand() (tea.Model, tea.Cmd) {
	line := m.input.Value()
	span, ok := expand.Find(line)
	if !ok {
		return m, nil
	}
	goal := strings.TrimSpace(span.Text)
	if goal == "" {
		return m, nil
	}
	m.span = span
	m.prefix = line[:span.Start]
	m.suffix = line[span.End:]
	m.genSeq = m.seq
	m.loading = true
	m.loadLabel = "候補を生成中"
	m.err = nil
	cmd, cancel := m.generateCmd(goal, m.genSeq)
	m.startInflight(cancel)
	return m, cmd
}

// lineContext は AI(...) の前後を LLM に渡す形にする。
// これが無いと、前段のパイプを無視した行全体を生成してしまう。
func (m Model) lineContext() llm.LineContext {
	return llm.LineContext{Prefix: m.prefix, Suffix: m.suffix}
}

// applyCandidate は選択中の候補を行に差し込む。前後はそのまま残す。
func (m *Model) applyCandidate() {
	if len(m.cands) == 0 {
		return
	}
	line := m.prefix + m.cands[m.cur].Command + m.suffix
	m.input.SetValue(line)
	m.input.CursorEnd()
}

// clearCandidates は候補の表示を消す。行の中身はそのまま。
func (m *Model) clearCandidates() {
	m.cands = nil
	m.cur = 0
	m.goal = ""
	m.err = nil
	m.loading = false
	m.probing = false
	m.explaining = false
	m.cancelInflight()
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.asking {
		return m.keyAsk(msg)
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		// 生成中なら生成だけを止める。5秒待って出たものを捨てて
		// 打ち直しになるのは損なので、行はそのまま残す。
		if m.loading {
			m.cancelInflight()
			m.loading = false
			m.err = nil
			return m, nil
		}
		// 候補が出ているなら候補だけ畳む。行は選択中の候補のまま残す。
		if len(m.cands) > 0 {
			m.clearCandidates()
			return m, nil
		}
		m.cancelInflight()
		m.Result = Result{Interrupt: true}
		return m, tea.Quit

	case tea.KeyCtrlD:
		if m.input.Value() == "" {
			m.cancelInflight()
			m.Result = Result{Quit: true}
			return m, tea.Quit
		}

	case tea.KeyEnter:
		line := strings.TrimSpace(m.input.Value())
		// 未展開の AI(...) をシェルに渡すと「そんなコマンドは無い」で終わる。
		// 待たずに済むよう、その場で展開を始める。
		if _, ok := expand.Find(line); ok && !m.loading {
			return m.tryExpand()
		}
		m.cancelInflight()
		m.Result = Result{Line: line, Submit: true}
		return m, tea.Quit

	case tea.KeyEsc:
		// 候補が出ているなら取り消して AI(...) の行に戻す。
		if len(m.cands) > 0 || m.loading {
			m.input.SetValue(m.prefix + "AI(" + m.goalOrSpan() + ")" + m.suffix)
			m.input.CursorEnd()
			m.clearCandidates()
			return m, nil
		}

	case tea.KeyCtrlO:
		// 深堀り。印字可能文字は必ず行の編集に使うので、? には割り当てない
		// (正規表現やグロブに ? を含む行が打てなくなる)。
		if len(m.cands) > 0 {
			m.asking = true
			m.ask.SetValue("")
			m.ask.Focus()
			return m, textinput.Blink
		}

	case tea.KeyTab:
		if len(m.cands) > 0 {
			return m.moveCandidate(1)
		}
		return m.completeWord()

	case tea.KeyShiftTab:
		if len(m.cands) > 0 {
			return m.moveCandidate(-1)
		}

	case tea.KeyUp:
		if len(m.cands) > 0 {
			return m.moveCandidate(-1)
		}
		if v, ok := m.history.Prev(); ok {
			m.input.SetValue(v)
			m.input.CursorEnd()
		}
		return m, nil

	case tea.KeyDown:
		if len(m.cands) > 0 {
			return m.moveCandidate(1)
		}
		if v, ok := m.history.Next(); ok {
			m.input.SetValue(v)
			m.input.CursorEnd()
		}
		return m, nil
	}

	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() == before {
		return m, cmd
	}

	// 行が変わったら候補も補完一覧も無効。世代を進めてタイマーを仕掛け直す。
	m.comps = nil
	if len(m.cands) > 0 || m.loading {
		m.clearCandidates()
	}
	m.seq++
	if expand.HasMarker(m.input.Value()) {
		return m, tea.Batch(cmd, idleCmd(m.seq))
	}
	return m, cmd
}

func (m Model) goalOrSpan() string {
	if m.goal != "" {
		return m.goal
	}
	return m.span.Text
}

func (m Model) moveCandidate(d int) (tea.Model, tea.Cmd) {
	m.cur = (m.cur + d + len(m.cands)) % len(m.cands)
	m.applyCandidate()
	if m.cands[m.cur].Explained {
		return m, nil
	}
	return m, explainTickCmd(m.genSeq, m.cands[m.cur].Strategy)
}

func (m Model) keyAsk(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		m.asking = false
		return m, nil
	case tea.KeyEnter:
		q := strings.TrimSpace(m.ask.Value())
		m.asking = false
		if q == "" || len(m.cands) == 0 {
			return m, nil
		}
		base := m.cands[m.cur]
		m.seq++
		m.genSeq = m.seq
		m.loading = true
		m.loadLabel = fmt.Sprintf("「%s」を掘り下げ中", truncate(q, 24))
		cmd, cancel := m.followupCmd(m.goal, base, q, m.genSeq)
		m.startInflight(cancel)
		return m, cmd
	}
	var cmd tea.Cmd
	m.ask, cmd = m.ask.Update(msg)
	return m, cmd
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// completeWord は Tab 補完を行う。
//
// 候補が1件なら確定させ、複数なら共通接頭辞まで進める。
// 接頭辞で進めなかったときだけ一覧を出す。毎回一覧を出すと画面が忙しい。
func (m Model) completeWord() (tea.Model, tea.Cmd) {
	line := m.input.Value()
	pos := m.input.Position()
	// textinput の位置はルーン単位なのでバイト位置に直す。
	bytePos := len(string([]rune(line)[:min(pos, len([]rune(line)))]))

	r := complete.Complete(line, bytePos)
	if len(r.Candidates) == 0 {
		m.comps = nil
		return m, nil
	}

	word := line[r.Start:r.End]
	insert := r.Candidates[0]
	if len(r.Candidates) > 1 {
		insert = complete.CommonPrefix(r.Candidates)
	}

	// 進めないなら一覧を見せる。bash と同じく、一度目の Tab で進めるところまで進め、
	// それ以上進まない二度目の Tab で一覧を出す。
	if insert == word || insert == "" {
		m.comps = r.Candidates
		return m, nil
	}

	// 1件に決まったときだけ空白を足して次の語へ進ませる。
	// ディレクトリは末尾が / なので、そのまま潜れるように空白は足さない。
	if len(r.Candidates) == 1 && !strings.HasSuffix(insert, "/") {
		insert += " "
	}
	m.comps = nil
	newLine := line[:r.Start] + insert + line[r.End:]
	m.input.SetValue(newLine)
	m.input.SetCursor(len([]rune(line[:r.Start] + insert)))
	return m, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
