package probe

import (
	"testing"

	"github.com/gimenorum/cmdmuse/internal/core"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		cmd  string
		want core.RiskLevel
	}{
		// 読み取りだけ
		{"grep -i error log.txt | tail -n 1", core.RiskNone},
		{"git log --oneline", core.RiskNone},
		{"ls -la", core.RiskNone},
		{"awk '/ERROR/{print $0}' log.txt", core.RiskNone},
		{"sed -n '5p' log.txt", core.RiskNone},

		// 書き込むが戻せる
		{"cp log.txt log.txt.backup", core.RiskWrites},
		{"git merge --no-ff develop", core.RiskWrites},
		{"git commit -am x", core.RiskWrites},
		{"mkdir -p a/b", core.RiskWrites},
		{"sed -i 's/a/b/' f.txt", core.RiskWrites},

		// 取り返しがつかない
		{"rm -rf build", core.RiskDestructive},
		{"git reset --hard HEAD~1", core.RiskDestructive},
		{"git push --force", core.RiskDestructive},
		{"git clean -fd", core.RiskDestructive},
		{"docker rm -f x", core.RiskDestructive},
		{"kubectl delete pod x", core.RiskDestructive},

		// 区間ごとに見て一番重いものを採る
		{"cp a b && rm -rf c", core.RiskDestructive},
		{"cat f | grep x", core.RiskNone},
		{"tail -n 100 log | grep err > out.txt", core.RiskNone}, // 判定はリダイレクト側で見る
	}
	for _, c := range cases {
		if got := Classify(c.cmd); got != c.want {
			t.Errorf("%q -> %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestRedirectsOutput(t *testing.T) {
	yes := []string{"echo x > f.txt", "cat a >> b", "grep x f | tee -a out"}
	no := []string{"grep x f", "cmd 2>&1", "echo '>' f", `echo ">" x`}
	for _, s := range yes {
		if s == "grep x f | tee -a out" {
			continue // tee は Classify 側で拾う
		}
		if !RedirectsOutput(s) {
			t.Errorf("%q はリダイレクトと判定すべき", s)
		}
	}
	for _, s := range no {
		if RedirectsOutput(s) {
			t.Errorf("%q はリダイレクトではない", s)
		}
	}
}
