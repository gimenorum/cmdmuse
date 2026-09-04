// Package expand は入力行から AI(...) を切り出す。
//
// 行のどこに書かれていてもよく、パイプの途中でも成立する:
//
//	cat server.log | AI(直近のエラーだけ抜き出したい) | less
//
// 置き換えるのは AI(...) の範囲だけで、前後はそのまま残す。
package expand

import "strings"

// Span は行の中の AI(...) の位置と中身。
type Span struct {
	Start int    // "AI(" の A の位置 (バイト)
	End   int    // 閉じ括弧の次の位置 (バイト)
	Text  string // 括弧の中身
}

// Find は行の中で最初に現れる AI(...) を返す。無ければ ok=false。
//
// 括弧の対応は入れ子と引用符を見て取る。閉じていない場合は未完成とみなし
// 見つからなかった扱いにする。打っている途中で発火させないため。
func Find(line string) (Span, bool) {
	for i := 0; i+3 <= len(line); i++ {
		if !matchMarker(line, i) {
			continue
		}
		open := i + 3 // "AI(" の次
		if end, ok := matchParen(line, open); ok {
			return Span{Start: i, End: end + 1, Text: line[open:end]}, true
		}
		// 閉じていないので、この位置は候補にならない。
		return Span{}, false
	}
	return Span{}, false
}

// matchMarker は line[i:] が AI( で始まり、直前が識別子の一部でないかを見る。
// FOOAI(...) のような偶然の一致を弾く。
func matchMarker(line string, i int) bool {
	if !strings.EqualFold(line[i:i+3], "ai(") {
		return false
	}
	if i > 0 && isIdentByte(line[i-1]) {
		return false
	}
	return true
}

func isIdentByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' || b == '_' || b == '-'
}

// matchParen は open の位置から始まる中身の、対応する閉じ括弧の位置を返す。
func matchParen(line string, open int) (int, bool) {
	depth := 1
	var quote byte
	for i := open; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// Replace は span を cmd で置き換えた行を返す。
func Replace(line string, s Span, cmd string) string {
	return line[:s.Start] + cmd + line[s.End:]
}

// HasMarker は AI( が書かれているかだけを見る。閉じているかは問わない。
// 「まだ閉じていないので待つ」を判定するために使う。
func HasMarker(line string) bool {
	for i := 0; i+3 <= len(line); i++ {
		if matchMarker(line, i) {
			return true
		}
	}
	return false
}
