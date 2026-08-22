# pprotein (chillins fork)

[kaz/pprotein](https://github.com/kaz/pprotein) の fork。ISUCON 本番で自チーム用に使うための変更を入れている。

upstream との差分:

- **Slack 通知** — グループ収集が揃ったら要約1通、収集/解析の失敗はその場で1通
- **`make build-linux`** — ISUCON サーバ向けの linux/amd64 クロスビルド
- **公開系ワークフローの削除** — `publish-release.yml`（public Release にバイナリ）と
  `push-docker-image.yml`（public GHCR に image）、および `.goreleaser.yaml` を削除した。
  成果物は [chillins/isucon-kit-v2](https://github.com/chillins/isucon-kit-v2)（private）経由でのみ配る

## 環境変数

`pprotein` 本体（`cli/pprotein`）が読むもの。

| 変数 | 必須 | 既定 | 用途 |
|---|---|---|---|
| `PORT` | | `9000` | listen ポート |
| `PPROTEIN_SLACK_WEBHOOK_URL` | | — | Incoming Webhook URL。**未設定なら通知機能ごと無効** |
| `PPROTEIN_BASE_URL` | | （空） | Slack のリンク先。例 `http://10.0.1.5:9000`。空ならリンクを省略 |
| `PPROTEIN_SLACK_GROUP_TIMEOUT` | | `5m` | グループの全ターゲットが揃うのを待つ上限 |

**webhook URL はリポジトリにコミットしない。** `isucon-kit-v2` の GitHub Secrets から CD が
`pprotein.env` を生成し、`/etc/pprotein.env` に rsync して systemd の `EnvironmentFile` で読ませる。

`pprotein-agent`（`cli/pprotein-agent`）は upstream のまま `PORT` / `PPROTEIN_HTTPLOG` /
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

```sh
make clean && make build-linux
cp dist/linux_amd64/pprotein dist/linux_amd64/pprotein-agent <isucon-kit-v2>/bin/
# isucon-kit-v2 側で PR を出す。main に入ると CD が EC2 に rsync して再起動する
```
