# クイックスタート

cloneから紙投票の送信まで。所要10分程度で、そのほとんどはスケジューラ待ちです。

## 1. 導入

```bash
git clone https://github.com/sol12378/keiba-masters-kit.git
cd keiba-masters-kit
make setup
```

`make setup` は `.venv` を作り、Pythonパッケージをeditableで入れ、`bin/votingd` と
`bin/votectl` をビルドします。

## 2. データなしでモデルを作る

```bash
make model
```

5つの段階が順に走ります。

| 段階 | 内容 | 典型的な出力 |
|---|---|---|
| `synth` | `data/synthetic` に合成シーズンを書き出す | 72レース、3連単の建値 約60,000通り |
| `train` | 前半の日付で2つの係数を推定 | `[0.935, 0.0]`、holdout NLL改善 約0.015 |
| `policy-table` | `(残りレース数, 所持pt) → 行動` を解く | 約27,000状態を1秒未満で |
| `verify` | 方策表を20,000回前向きに回す | 到達率 約0.41、最終残高の平均 約810,000 |
| `backtest` | パネルを方策表で再生 | 1経路あたり的中は多くて1〜2回 |

何より先に見るべき数字が2つあります。

`upper_bound_on_reach` は `R_max × 初期資金 / 目標` です。資金が回収率の範囲でしか
保存されない以上、どんな方策もこれを超えられません。`value_at_initial_bank` はこれを
下回っていなければなりません。下回らない場合、ソルバは表を返さずに例外を投げます。
上界を超える値は、戦略ではなく**動的計画が自分の離散化から資金を作っている**ことを
意味するからです。

`mean_final_bank` は初期資金を下回ります。常にそうなります。このパイプラインのどこにも
エッジは存在しないからです。

## 3. 全経路をローカルで回す

```bash
make demo
```

デモがやること。

1. 当日の状態ディレクトリを `var/` 以下でクリアする
2. ある1日のレースを10分後の発走に付け替える
3. その日付のポリシーを生成し、計画バンドルを作る
4. 紙投票ドライバで `votingd` を起動する
5. バンドルのSHA-256を明示して、その日をimport・武装する
6. デーモンを起動したまま残すので、動作を観察できる

別のシェルで:

```bash
./bin/votectl --policy var/policy_<date>.json status
cat var/voting-<date>/paper_state.json
tail -f var/voting-<date>/events.jsonl
```

各レースは `DISCOVERED → VALIDATED → ARMED → POSTING → PENDING_CONFIRMATION →
CONFIRMED` と遷移します。送信は発走300秒前、読み返しによる確認はその約1分後です。

`paper_state.json` は紙投票ドライバの台帳です。受け付けた投票と、1,000,000から始まる
模擬残高が入っています。残高は計画が賭けた額だけ正確に減るので、**送信された内容が
プランナーの決定と一致しているか**を最も手早く確認できる場所です。

Ctrl-Cでデモを止めます。

## 4. 自分のデータを使う

パイプラインはレースごとのJSONファイルが並んだディレクトリを読みます。同じ形を出力する
収集スクリプトを自分で書けば、そのまま使えます。

```bash
.venv/bin/python -m pykeiba train    --panel /path/to/panel --out out/model.json
.venv/bin/python -m pykeiba backtest --panel /path/to/panel --model out/model.json \
    --table out/policy_table.json
```

形式と、`captured_at` について守らなければならないことは
[04-data-contract.md](04-data-contract.md) にあります。

## 5. launchdの下で動かす

```bash
./scripts/launchd.sh install --policy var/policy_<date>.json --driver paper
./scripts/launchd.sh status
./scripts/launchd.sh logs
./scripts/launchd.sh uninstall
```

[03-launchd.md](03-launchd.md) を参照してください。特にスリープの項は読んでおいて
ください。
