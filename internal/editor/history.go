package editor

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// History は入力履歴。起動時にファイルから読み、終了時に書き戻す。
type History struct {
	items []string
	pos   int // items の末尾+1 が「履歴を辿っていない」状態
	path  string
}

func LoadHistory() *History {
	h := &History{}
	if dir, err := os.UserHomeDir(); err == nil {
		h.path = filepath.Join(dir, ".cmdmuse_history")
	}
	if h.path != "" {
		if f, err := os.Open(h.path); err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				if t := strings.TrimRight(sc.Text(), "\r\n"); t != "" {
					h.items = append(h.items, t)
				}
			}
		}
	}
	if len(h.items) > 2000 {
		h.items = h.items[len(h.items)-2000:]
	}
	h.pos = len(h.items)
	return h
}

// Add は履歴に積む。直前と同じなら重ねない。
func (h *History) Add(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		h.pos = len(h.items)
		return
	}
	if n := len(h.items); n > 0 && h.items[n-1] == line {
		h.pos = len(h.items)
		return
	}
	h.items = append(h.items, line)
	h.pos = len(h.items)
}

func (h *History) Prev() (string, bool) {
	if h.pos == 0 {
		return "", false
	}
	h.pos--
	return h.items[h.pos], true
}

func (h *History) Next() (string, bool) {
	if h.pos >= len(h.items) {
		return "", false
	}
	h.pos++
	if h.pos == len(h.items) {
		return "", true // 末尾より先は空行
	}
	return h.items[h.pos], true
}

// Recent は直近 n 件を古い順で返す。LLM に渡す文脈に使う。
func (h *History) Recent(n int) []string {
	if n > len(h.items) {
		n = len(h.items)
	}
	return append([]string(nil), h.items[len(h.items)-n:]...)
}

func (h *History) Save() {
	if h.path == "" || len(h.items) == 0 {
		return
	}
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, it := range h.items {
		w.WriteString(it)
		w.WriteByte('\n')
	}
	w.Flush()
}
