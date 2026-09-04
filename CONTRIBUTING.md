# 開発の進め方

## ブランチ

`main` から作業ブランチを切り、PR で戻す (GitHub Flow)。`main` へは直接 push しない。

ブランチ名は種類を接頭辞にする。

```
feat/completion       機能追加
fix/probe-glob        不具合修正
docs/model-guide      ドキュメント
refactor/editor       挙動を変えない整理
test/probe-safety     テストだけの追加
ci/github-actions     CI・ビルド周り
```

## コミット

[Conventional Commits](https://www.conventionalcommits.org/) に従う。

```
feat: delegate completion to compgen
fix: expand globs in probe arguments
docs: add model selection guide
```

**PR はマージコミットで取り込まれるため、途中のコミットもそのまま `main` に残る。**
`wip` や `typo` のようなコミットを作らず、1つずつ意味のある単位で刻むこと。
散らかったら push 前に `git rebase -i` で整理する。

本文には「何をしたか」ではなく「なぜそうしたか」を書く。差分を見れば何をしたかは分かる。

## PR

マージには次の両方が必要。

- **`gimenorum` の承認** — 承認1件が必須。承認後に push すると再承認が要る
- **CI が通ること** — `gofmt` / `go vet` / `go test -race` / 5構成のクロスビルド

`main` は force push とブランチ削除を禁止している。

## 手元での確認

PR を出す前にこれを通しておく。

```bash
gofmt -l .          # 出力が空であること
go vet ./...
go test -race ./...
```

## 設計上の約束

このツールは LLM の生成物を実行する場所があるので、次の2点は動かさない。

**probe はシェルを介さない。** `internal/probe` はモデルが生成した文字列を実行する。
`exec` に argv を直接渡し、コマンド名とサブコマンドを許可リストで絞る。
新しいコマンドを許可リストに足すときは、それが**副作用を持たない**ことを確認すること。
`internal/probe/probe_test.go` の危険入力テストは消さない。

**フラグの意味を LLM に生成させない。** `internal/spec` が `--help` と `man` から取った
実定義だけを context に注入し、モデルには要約だけをさせる。
定義が取れなかったフラグは「取得できませんでした」と書かせる。
ここを緩めると、それらしいが間違った解説が出るようになる。
