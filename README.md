# cbdprotein (chillins fork)

[kaz/pprotein](https://github.com/kaz/pprotein) の fork。ISUCON 本番で自チーム用に使うための変更を入れている。

upstream との差分:

- **Slack 通知** — グループ収集が揃ったら要約1通、収集/解析の失敗はその場で1通
- **`make build-linux`** — ISUCON サーバ向けの linux/amd64 クロスビルド
- **`deliver-to-kit.yml`** — main へのマージで linux/amd64 をビルドし、`isucon-kit-v2` の
  `bin/` を更新する PR を自動で出す
- **公開系ワークフローの削除** — `publish-release.yml`（public Release にバイナリ）と
  `push-docker-image.yml`（public GHCR に image）、および `.goreleaser.yaml` を削除した。
  成果物は [chillins/isucon-kit-v2](https://github.com/chillins/isucon-kit-v2)（private）経由でのみ配る

## 環境変数

`cbdprotein` 本体（`cli/pprotein`）が読むもの。

| 変数 | 必須 | 既定 | 用途 |
|---|---|---|---|
| `PORT` | | `9000` | listen ポート |
| `PPROTEIN_SLACK_WEBHOOK_URL` | | — | Incoming Webhook URL。**未設定なら通知機能ごと無効** |
| `PPROTEIN_SLACK_GROUP_TIMEOUT` | | `5m` | グループの全ターゲットが揃うのを待つ上限 |

**webhook URL はリポジトリにコミットしない。** `isucon-kit-v2` の GitHub Secrets から CD が
`cbdprotein.env` を生成し、`/etc/cbdprotein.env` に rsync して systemd の `EnvironmentFile` で読ませる。

`cbdprotein-agent`（`cli/pprotein-agent`）は upstream のまま `PORT` / `PPROTEIN_HTTPLOG` /
`PPROTEIN_SLOWLOG` / `PPROTEIN_GIT_REPOSITORY` を読む。

## 通知の挙動

- **グループ収集** (`GET /api/group/collect`) … `targets.json` の全ターゲットが終わった時点で要約1通。
  1つでも失敗/未着があれば ⚠️ になる
- **失敗** … グループかどうかに関わらず、その場で1通
- **単発 collect の成功** … 通知しない（`POST /api/pprof` などを直接叩いたケース）
- **起動時の再処理** … 通知しない。ここを通知すると再起動のたびに過去スナップショット全件が流れる

## ビルド

```sh
# ローカル実行（macOS でも動く）
make run

# ISUCON サーバ向け。dist/linux_amd64/ に2バイナリが出る
make build-linux
```

`view/dist` は「ディレクトリが無ければビルドする」ターゲットなので、**フロントを変えたときは
`make clean` を挟む**（さもないと古い `dist` が embed される）。

## isucon-kit-v2 への配布

main に PR がマージされると `.github/workflows/deliver-to-kit.yml` が走り、
linux/amd64 をビルドして [chillins/isucon-kit-v2](https://github.com/chillins/isucon-kit-v2) に
`bin/cbdprotein` / `bin/cbdprotein-agent` を更新する PR を出す。

- ブランチは `deliver/cbdprotein` 固定。main が更新されるたび kit の main を土台に force-push で
  作り直すので、**開いている PR は常に1本**（マージ待ちの間に追加のマージがあれば同じ PR が更新される）
- バイナリに差分が無いマージ（ドキュメントのみなど）では PR を作らない
- 由来は PR 内の `bin/cbdprotein.version` から追える
- 手動で流したいときは Actions から `workflow_dispatch`

### 必要な secret

cbdprotein の Actions secret に `KIT_REPO_TOKEN` を登録する。`isucon-kit-v2` に対して
**Contents: Read and write** / **Pull requests: Read and write** を持つ fine-grained PAT
（`GITHUB_TOKEN` は他リポジトリに push できないため）。

```sh
gh secret set KIT_REPO_TOKEN --repo chillins/cbdprotein
```

### 受け入れ側（未対応）

`isucon-kit-v2` の `Makefile` の `cbdprotein/install-*` は今も upstream `kaz/pprotein` の
release を wget しており、`deploy.sh` も `bin/` を rsync していない。
**この PR をマージしただけでは EC2 には反映されない。** kit 側の配線は別途:

- `deploy.sh` に `bin/` の rsync（`isuconapp` と `isucondb1` / `isucondb2`）を追加
- `Makefile` の `install ./cbdprotein ...` を `bin/` 参照に変更
- systemd ユニットを `cbdprotein.service` / `cbdprotein-agent.service` にし、実行ファイルを `/usr/local/bin/cbdprotein` / `/usr/local/bin/cbdprotein-agent` に
- Slack webhook 用の `cbdprotein.env` 生成と `/etc/cbdprotein.env` への配置

### 手動でやる場合

```sh
make clean && make build-linux
cp dist/linux_amd64/cbdprotein dist/linux_amd64/cbdprotein-agent <isucon-kit-v2>/bin/
# isucon-kit-v2 側で PR を出す
```
