# MOCHIFIGHT-backend

3Dアクション対戦ゲーム「MOCHIFIGHT」のバックエンドサーバー。Go言語(標準ライブラリのみ)で実装したリアルタイム対戦マッチング・バトルサーバーです。

## アーキテクチャ

対戦相手を待つ「マッチング」と、対戦中のリアルタイム通信を行う「バトル」を、通信要件の違いに応じて別プロトコル・別プロセスに分離しています。

```
                        ┌─────────────────────────┐
  Client A/B  ───TCP───▶│  MakeRoom Server  :8080  │
                        │  (部屋待機・満員通知)      │
                        └─────────────────────────┘

                        ┌─────────────────────────┐
  Client A/B  ───UDP───▶│  Battle Server    :9052  │
                        │  (座標/攻撃の中継・HP管理) │
                        └─────────────────────────┘
```

| サーバー | プロトコル | ポート | 役割 |
|---|---|---|---|
| MakeRoom | TCP | `:8080` | クライアントを最大2人まで受け付け、2人揃ったら`match_ready`を通知する |
| Battle | UDP | `:9052` | 対戦中の座標・攻撃イベントを相手に中継し、HP・死亡判定を管理する |

**なぜプロトコルを使い分けているか**

- **MakeRoom (TCP)**: 「部屋が満員になった」という状態変化を確実に届ける必要があるため、信頼性のあるTCPを使用。
- **Battle (UDP)**: プレイヤー座標や攻撃情報は毎フレーム送信される高頻度データで、多少のパケットロスより低遅延を優先すべきため、コネクションレスなUDPを使用。またUDPは切断を検知できないため、`lastSeen`のタイムスタンプを使ったタイムアウト監視(`evictStaleClients`)で不要になったクライアントを自動的に部屋から除去している。

## セットアップ・起動方法

```bash
go run ./MakeRoom   # 対戦相手を待機するサーバー
go run ./Battle     # 対戦中の処理を行うサーバー
```

Windowsでは両方をまとめて起動するバッチファイルも用意しています。

```bash
start_servers.bat
```

## テストの実行方法

```bash
go test ./...
```

## ディレクトリ構成

```
MakeRoom/main.go       TCPマッチングサーバー
MakeRoom/main_test.go  MakeRoomのユニットテスト
Battle/main.go         UDPバトルサーバー本体(座標中継・HP管理・タイムアウト監視)
Battle/HPManager.go    HP更新/死亡/ウェルカムメッセージの型・定数定義
Battle/main_test.go    Battleのユニットテスト
```

## 通信プロトコル仕様

### MakeRoom (TCP, `:8080`)

接続するだけで部屋に登録される。2人揃うと全員に以下を送信する。

```json
{"type": "match_ready", "connected": 2, "description": "2 players connected"}
```

### Battle (UDP, `:9052`)

初回パケット受信時にサーバーがクライアントを登録し、以下を返す。

```json
{"type": "welcome", "id": "<クライアントのアドレス文字列>"}
```

| メッセージ | 方向 | 形式 | 内容 |
|---|---|---|---|
| `P:...` | Client → Server → 相手Client | 生文字列 | プレイヤー座標。低遅延優先で軽量なプレフィックス形式を使用 |
| `S:...` | Client → Server → 相手Client | 生文字列 | 弾(Shot)の座標・向き |
| `G:...` | Client → Server → 相手Client | 生文字列 | 掴み技(Grapple)の情報 |
| `hit_report` | Client → Server | JSON | `{"type":"hit_report","target":"...","damage":20}` 自己申告のヒット通知。サーバーはこれを信頼してHPを減算する(下記「既知の課題」参照) |
| `hp_update` | Server → 全Client | JSON | `{"type":"hp_update","target":"...","hp":80}` |
| `death` | Server → 全Client | JSON | `{"type":"death","target":"..."}` HPが0になったプレイヤーを通知 |
| `disconnect` | Client → Server | 生文字列 | 明示的な退室 |

## 既知の課題

- **サーバー権威の当たり判定が未実装**: `hit_report`はクライアントの自己申告をそのまま信頼しており、サーバー側で座標をもとにした検証は行っていない。チート対策の観点で改善余地あり。
