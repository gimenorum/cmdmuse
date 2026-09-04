package core

// ProbeState は候補の実行可能性の判定結果。
type ProbeState int

const (
	ProbePending ProbeState = iota
	ProbeOK
	ProbeFail
	ProbeSkip // 検証手段がない
)

// Precond は候補が成立するための前提条件と、それを確かめる副作用のないコマンド。
type Precond struct {
	Desc  string `json:"desc"`
	Probe string `json:"probe"`

	State  ProbeState `json:"-"`
	Output string     `json:"-"`
}

// FlagDoc は候補が使っているフラグ1つ分の解説。
// Def は --help / man から取った実定義で、LLM に書き換えさせない。
type FlagDoc struct {
	Flag string
	Def  string
	Note string
}

// Candidate は「同じ目的を別のやり方で達成する」選択肢1つ。
type Candidate struct {
	// Strategy は戦略ラベル。文字列ではなくこれで重複排除する。
	Strategy string `json:"strategy"`
	Command  string `json:"command"`
	Summary  string `json:"summary"`
	// Axis は他候補との違いが何の軸なのか (移植性 / 破壊性 / スコープ など)。
	Axis     string    `json:"axis"`
	Preconds []Precond `json:"preconds"`

	Flags       []FlagDoc `json:"-"`
	Explanation string    `json:"-"`
	Explained   bool      `json:"-"`

	// Risk は副作用の重さ。コマンドは実行しないが、選ぶ前に分かる必要がある。
	Risk RiskLevel `json:"-"`
}

// RiskLevel は候補コマンドが持つ副作用の重さ。
type RiskLevel int

const (
	RiskNone RiskLevel = iota
	RiskWrites
	RiskDestructive
)

func (r RiskLevel) Label() string {
	switch r {
	case RiskWrites:
		return "書き込み"
	case RiskDestructive:
		return "破壊的"
	}
	return ""
}

// State は前提条件の集計。1つでも落ちれば Fail。
func (c *Candidate) State() ProbeState {
	if len(c.Preconds) == 0 {
		return ProbeSkip
	}
	worst := ProbeOK
	for _, p := range c.Preconds {
		switch p.State {
		case ProbeFail:
			return ProbeFail
		case ProbePending:
			worst = ProbePending
		}
	}
	return worst
}

// FailedPreconds は満たされなかった前提条件を返す。
func (c *Candidate) FailedPreconds() []Precond {
	var out []Precond
	for _, p := range c.Preconds {
		if p.State == ProbeFail {
			out = append(out, p)
		}
	}
	return out
}

// SessionContext は直前のシェル操作。候補生成の文脈に混ぜる。
type SessionContext struct {
	Source   string // tmux / transcript / history / none
	Cwd      string
	Shell    string
	OS       string
	Recent   []string
	LastExit int
	HasExit  bool
}
