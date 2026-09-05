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

	// 補完の候補一覧と、いま選んでいるもの。
	// Tab を押すたびに compCur が進み、選択中の候補が ghost として出る。
	comps     []string
	compCur   int
	compStart int // 置き換える範囲 (バイト)
	compEnd   int
	compOn    bool // Tab で一覧を開いて選択している最中

	// ghost はカーソルの先に出す補完の提案。行にはまだ入っていない。
	// compOn なら選択中の候補を白く、そうでなければ共通接頭辞を薄く出す。
	ghost string

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

// ghostMsg は非同期に計算した提案。
// 補完はファイルシステムを触るので、遅いパスを踏んでも
// 打鍵が止まらないよう Update ループの外で計算する。
type ghostMsg struct {
	seq  int
	line string
	text string
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

	case ghostMsg:
		// 打鍵が進んでいたら古い計算結果。捨てる。
		if msg.seq != m.seq || m.compOn || m.input.Value() != msg.line {
			return m, nil
		}
		m.ghost = msg.text
		return m, nil

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
		// 選択中の候補があるなら、まず行に取り込む。実行はしない。
		if m.compOn && len(m.comps) > 0 {
			return m.commitCompletion()
		}
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
		// 候補の選択を取り消して、薄い提案に戻す。
		if m.compOn {
			m.closeCompletion()
			return m, m.refreshGhost()
		}
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
		return m.cycleCompletion(1)

	case tea.KeyShiftTab:
		if len(m.cands) > 0 {
			return m.moveCandidate(-1)
		}
		if m.compOn {
			return m.cycleCompletion(-1)
		}

	case tea.KeyUp:
		if len(m.cands) > 0 {
			return m.moveCandidate(-1)
		}
		// 一覧はグリッドなので、上下は1行ぶん動かす。
		if m.compOn {
			return m.moveCompletionRow(-1)
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
		if m.compOn {
			return m.moveCompletionRow(1)
		}
		if v, ok := m.history.Next(); ok {
			m.input.SetValue(v)
			m.input.CursorEnd()
		}
		return m, nil

	case tea.KeyLeft:
		if m.compOn {
			return m.cycleCompletion(-1)
		}

	case tea.KeyRight:
		if m.compOn {
			return m.cycleCompletion(1)
		}
	}

	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() == before {
		return m, cmd
	}

	// 行が変わったら候補も補完一覧も無効。世代を進めてタイマーを仕掛け直す。
	m.closeCompletion()
	if len(m.cands) > 0 || m.loading {
		m.clearCandidates()
	}
	m.seq++
	// seq を進めてから仕掛ける。古い結果を捨てる判定に使う。
	ghostCmd := m.refreshGhost()
	if expand.HasMarker(m.input.Value()) {
		return m, tea.Batch(cmd, ghostCmd, idleCmd(m.seq))
	}
	return m, tea.Batch(cmd, ghostCmd)
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

// ---- 補完 ----

// cycleCompletion は Tab / Shift+Tab で候補を送る。
//
// 初回の Tab で候補を集めて先頭を選び、以降は選択が動く。
// AI(...) の候補が Tab で切り替わるのと同じ操作感になるよう揃えてある。
func (m Model) cycleCompletion(d int) (tea.Model, tea.Cmd) {
	if !m.compOn {
		line := m.input.Value()
		r := complete.Complete(line, m.bytePos())
		if len(r.Candidates) == 0 {
			return m, nil
		}
		m.comps = r.Candidates
		m.compStart, m.compEnd = r.Start, r.End
		m.compCur = 0
		m.compOn = true
	} else {
		m.compCur = (m.compCur + d + len(m.comps)) % len(m.comps)
	}
	m.ghost = m.selectedSuffix()
	return m, nil
}

// commitCompletion は選択中の候補を行に取り込む。実行はしない。
func (m Model) commitCompletion() (tea.Model, tea.Cmd) {
	line := m.input.Value()
	pick := m.comps[m.compCur]
	// ディレクトリはそのまま潜れるよう空白を足さない。
	if !strings.HasSuffix(pick, "/") {
		pick += " "
	}
	newLine := line[:m.compStart] + pick + line[m.compEnd:]
	m.input.SetValue(newLine)
	m.input.SetCursor(len([]rune(line[:m.compStart] + pick)))
	m.closeCompletion()
	return m, m.refreshGhost()
}

func (m *Model) closeCompletion() {
	m.comps = nil
	m.compCur = 0
	m.compOn = false
	m.ghost = ""
}

// selectedSuffix は選択中の候補のうち、まだ打っていない部分を返す。
func (m Model) selectedSuffix() string {
	if len(m.comps) == 0 {
		return ""
	}
	pick := m.comps[m.compCur]
	word := m.input.Value()[m.compStart:m.compEnd]
	if !strings.HasPrefix(pick, word) {
		return pick
	}
	return pick[len(word):]
}

// bytePos は textinput のルーン単位のカーソル位置をバイト位置に直す。
func (m Model) bytePos() int {
	r := []rune(m.input.Value())
	p := m.input.Position()
	if p > len(r) {
		p = len(r)
	}
	return len(string(r[:p]))
}

// refreshGhost は提案を消して、計算し直す Cmd を返す。
//
// 補完はファイルシステムを触る。WSL の PATH には Windows 側が数十個
// 入っていて ReadDir に数秒かかることがあり、Update の中で待つと
// その間の打鍵が固まって溜まる。計算は別 goroutine に出す。
//
// カーソルが行末にないときは出さない。途中に差し込むと、消したいのか
// 続けたいのかが読めず邪魔になるため。zsh-autosuggestions も同じ扱い。
func (m *Model) refreshGhost() tea.Cmd {
	if m.compOn {
		return nil
	}
	m.ghost = ""

	line := m.input.Value()
	if line == "" || m.input.Position() != len([]rune(line)) {
		return nil
	}
	// AI(...) を書いている最中は展開の対象なので、補完で邪魔しない。
	if expand.HasMarker(line) {
		return nil
	}

	seq := m.seq
	return func() tea.Msg {
		return ghostMsg{seq: seq, line: line, text: suggestFor(line)}
	}
}

// suggestFor は行に対する提案を返す。呼び出しはブロックする。
func suggestFor(line string) string {
	r := complete.Complete(line, len(line))
	if len(r.Candidates) == 0 {
		return ""
	}
	word := line[r.Start:]
	insert := r.Candidates[0]
	if len(r.Candidates) > 1 {
		insert = complete.CommonPrefix(r.Candidates)
	}
	if !strings.HasPrefix(insert, word) || len(insert) <= len(word) {
		return ""
	}
	return insert[len(word):]
}

// moveCompletionRow は一覧のグリッドを1行ぶん上下に動かす。
//
// 左右が1件ずつ動くのに対し、上下は見た目の行に合わせる。
// 端は回り込ませず止める。グリッドで縦に回り込むと今どこにいるか分からなくなる。
func (m Model) moveCompletionRow(d int) (tea.Model, tea.Cmd) {
	if !m.compOn || len(m.comps) == 0 {
		return m, nil
	}
	cols := gridCols(m.comps, m.width)
	next := m.compCur + d*cols
	if next < 0 || next >= len(m.comps) {
		return m, nil
	}
	m.compCur = next
	m.ghost = m.selectedSuffix()
	return m, nil
}
