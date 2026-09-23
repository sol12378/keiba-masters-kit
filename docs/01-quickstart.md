# クイックスタート

リポジトリの取得から、模擬投票で実際に送信されるところまでを順に説明します。所要時間は10分ほどで、そのほとんどは発走時刻を待つ時間です。

## 1. 導入

```bash
git clone https://github.com/sol12378/keiba-masters-kit.git
cd keiba-masters-kit
make setup
```

`make setup` は、Python の仮想環境 `.venv` を作ってパッケージを入れ、`bin/votingd`（投票デーモン）と `bin/votectl`（操作用コマンド）をビルドします。

## 2. データなしでモデルを作る

```bash
make model
```

次の5つの処理が順に実行されます。

| 処理 | 内容 | 出力の目安 |
|---|---|---|
| `synth` | `data/synthetic` に合成データを書き出す | 72レース、三連単のオッズ約60,000通り |
| `train` | 前半の日付で2つの重みを推定する | 重み `[0.935, 0.0]`、holdout NLL の改善 約0.015 |
| `policy-table` | 「残りレース数・所持ポイント → 行動」の表を計算する | 約27,000状態を1秒未満で |
| `verify` | できた表で20,000回シミュレーションする | 到達率 約0.41、最終残高の平均 約810,000 |
| `backtest` | 合成データのレースを表に従って再生する | 的中は1回の再生で多くて1〜2回 |

最初に確認していただきたい数字が2つあります。

1つ目は `upper_bound_on_reach` です。これは `最大払戻率 × 初期資金 ÷ 目標` で、どのような賭け方をしてもこの確率を超えて目標に届くことはありません。計算結果の `value_at_initial_bank` は必ずこれを下回ります。もし上回った場合、それは有効な戦略ではなく計算の誤りなので、プログラムは表を出力せずに停止します。

2つ目は `mean_final_bank`（最終残高の平均）です。初期資金の1,000,000を下回ります。この手法には市場を上回る優位性がないため、平均すると必ず損をします。

## 3. 投票の流れをローカルで再現する

```bash
make demo
```

デモは次の順に進みます。

1. `var/` 以下にある当日分の状態を消去する
2. ある1日のレースの発走時刻を、10分後から始まるように付け替える
3. その日付用のポリシーファイルを作り、計画ファイルを作る
4. 模擬投票ドライバで `votingd` を起動する
5. 計画を取り込み、ハッシュ値を入力してその日の送信を許可する
6. デーモンを動かしたままにして、送信の様子を観察できるようにする

別のターミナルで、次のように様子を確認できます。

```bash
./bin/votectl --policy var/policy_<日付>.json status
cat var/voting-<日付>/paper_state.json
tail -f var/voting-<日付>/events.jsonl
```

各レースの状態は `DISCOVERED → VALIDATED → ARMED → POSTING → PENDING_CONFIRMATION → CONFIRMED` と進みます。送信は発走の300秒前に行われ、その約1分後に大会側（ここでは模擬）の記録を読み返して照合します。

`paper_state.json` は模擬投票の台帳です。受け付けた投票と、1,000,000から始まる模擬の残高が記録されています。残高は計画どおりの金額だけ減るので、送信内容が計画と一致しているかを手早く確認できます。

止めるときは Ctrl-C を押してください。

## 4. ご自身のデータを使う

パイプラインは、1レースにつき1つのJSONファイルが並んだディレクトリを読み込みます。同じ形式のファイルを出力するように収集の仕組みを用意すれば、そのまま使えます。

```bash
.venv/bin/python -m pykeiba train    --panel /path/to/panel --out out/model.json
.venv/bin/python -m pykeiba backtest --panel /path/to/panel --model out/model.json \
    --table out/policy_table.json
```

データ形式と、とくに `captured_at`（オッズを取得した時刻）について守る必要があることは、[04-data-contract.md](04-data-contract.md) にまとめています。

## 5. launchd で常駐させる

```bash
./scripts/launchd.sh install --policy var/policy_<日付>.json --driver paper
./scripts/launchd.sh status
./scripts/launchd.sh logs
./scripts/launchd.sh uninstall
```

詳しくは [03-launchd.md](03-launchd.md) をご覧ください。Mac がスリープすると投票できなくなる点について書いた節は、常駐させる前にぜひお読みください。
