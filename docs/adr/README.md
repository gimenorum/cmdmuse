# アーキテクチャ決定記録 (ADR)

ADR は Architecture Decision Record の略で、重要な設計判断を1件1ファイルで記録したもの。
書式は [Architectural Decision Records](https://adr.github.io/) に従い、
**状況 / 決定 / 検討した代替案 / 結果** の順で書く。

決定を差し替えるときは既存のファイルを書き換えず、新しい番号で追加して
古いものの状態を「破棄」に変える。判断の履歴を残すため。

全体の設計は [../DESIGN.md](../DESIGN.md) を参照。

| 番号 | 決定 | 状態 |
|---|---|---|
| [ADR-1](./0001-probe-allowlist.md) | probe をシェルに通さず、許可リストで実行する | 採用 |
| [ADR-2](./0002-no-compgen.md) | 補完を compgen に委ねない | 採用 |
| [ADR-3](./0003-inject-flag-definitions.md) | フラグの実定義を注入し、要約だけをさせる | 採用 |
| [ADR-4](./0004-no-alternate-screen.md) | 端末の代替画面に入らず、行エディタとして動く | 採用 |
| [ADR-5](./0005-strategy-label-dedup.md) | 候補の重複判定に「コマンド名 + サブコマンド + 最初のフラグ」を使う | 採用 |
| [ADR-6](./0006-no-printable-keys-for-actions.md) | 行に打てる文字を操作キーに割り当てない | 採用 |
| [ADR-7](./0007-staged-ctrl-c.md) | `Ctrl+C` は状態によって捨てる範囲を変える | 採用 |
| [ADR-8](./0008-line-fragment-only.md) | モデルには行の断片だけを書かせる | 採用 |
