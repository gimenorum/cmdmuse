package probe

import (
	"strings"

	"github.com/gimenorum/cmdmuse/internal/core"
)

// 副作用の重さの判定。プローブと違いコマンドは実行しないが、
// 選ぶ前に分かる必要があるので静的に見る。

// 取り返しがつかない操作。確認なしに選ばせたくない。
var destructive = map[string]bool{
	"rm": true, "rmdir": true, "shred": true, "dd": true, "mkfs": true,
	"truncate": true, "chown": true, "chmod": true, "kill": true, "pkill": true,
	"killall": true, "reboot": true, "shutdown": true, "fdisk": true, "parted": true,
}

// ファイルや状態を変えるが、概ね元に戻せる操作。
var writes = map[string]bool{
	"mv": true, "cp": true, "mkdir": true, "touch": true, "ln": true,
	"tee": true, "install": true, "rename": true, "sed": true, "tar": true,
	"unzip": true, "gzip": true, "gunzip": true, "curl": true, "wget": true,
}

// サブコマンドまで見ないと判定できないもの。
var subcommandRisk = map[string]map[string]core.RiskLevel{
	"git": {
		"reset": core.RiskDestructive, "clean": core.RiskDestructive, "rebase": core.RiskDestructive,
		"push": core.RiskDestructive, "filter-branch": core.RiskDestructive,
		"merge": core.RiskWrites, "commit": core.RiskWrites, "add": core.RiskWrites,
		"checkout": core.RiskWrites, "switch": core.RiskWrites, "restore": core.RiskWrites,
		"branch": core.RiskWrites, "stash": core.RiskWrites, "cherry-pick": core.RiskWrites,
		"init": core.RiskWrites, "clone": core.RiskWrites, "mv": core.RiskWrites, "rm": core.RiskDestructive,
	},
	"docker":  {"rm": core.RiskDestructive, "rmi": core.RiskDestructive, "prune": core.RiskDestructive, "stop": core.RiskWrites},
	"kubectl": {"delete": core.RiskDestructive, "apply": core.RiskWrites, "scale": core.RiskWrites},
	"npm":     {"install": core.RiskWrites, "uninstall": core.RiskWrites, "publish": core.RiskDestructive},
	"pip":     {"install": core.RiskWrites, "uninstall": core.RiskDestructive},
}

// Classify はコマンド行の副作用の重さを判定する。
// パイプや && で繋がった各区間を見て、最も重いものを返す。
func Classify(cmdline string) core.RiskLevel {
	worst := core.RiskNone
	for _, seg := range splitSegments(cmdline) {
		if r := classifySegment(seg); r > worst {
			worst = r
		}
	}
	return worst
}

func classifySegment(seg string) core.RiskLevel {
	fields := strings.Fields(seg)
	for len(fields) > 0 && strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "-") {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return core.RiskNone
	}
	name := fields[0]
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}

	if subs, ok := subcommandRisk[name]; ok {
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "-") {
				continue
			}
			if r, ok := subs[f]; ok {
				return r
			}
			break
		}
	}
	if destructive[name] {
		return core.RiskDestructive
	}
	// sed -i はその場で書き換えるので、読み取り用途の sed とは別扱い。
	if name == "sed" && !hasFlag(fields, "-i") {
		return core.RiskNone
	}
	if writes[name] {
		return core.RiskWrites
	}
	return core.RiskNone
}

func hasFlag(fields []string, flag string) bool {
	for _, f := range fields[1:] {
		if f == flag || strings.HasPrefix(f, flag) {
			return true
		}
	}
	return false
}

// splitSegments はパイプや連結演算子でコマンド行を区切る。
// 引用符の中の記号は区切りにしない。
func splitSegments(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune

	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			cur.WriteRune(r)
		case r == '|' || r == '&' || r == ';':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// RedirectsOutput は > や >> でファイルを書き換えるかを見る。
func RedirectsOutput(cmdline string) bool {
	var quote rune
	for i, r := range cmdline {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '>':
			// 2>&1 のようなfd複製は書き込みではない。
			rest := cmdline[i+1:]
			if strings.HasPrefix(strings.TrimSpace(rest), "&") {
				continue
			}
			return true
		}
	}
	return false
}
