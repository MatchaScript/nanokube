# contract

Controller レーン(Go)と Local レーン(Rust)の間の言語間契約の置き場。

- `fixtures/`: desired document の golden fixture。Go 側(internal/render)が
  生成し、Rust 側(agent/)のテストが同じファイルを読む。契約のドリフトは
  この共有によって CI で検出する。
- desired の gRPC 契約(.proto)もここに置く(Step 1 実装項目3で定義される)。

参照: docs/nanokube/2026-07-06-nanokube-component-architecture-rev5.md
(「desired は一枚のドキュメント」「bootstrap・transport・below-cluster」)
