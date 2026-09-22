# 提出コード — AI競馬予想マスターズ2026

このディレクトリは**凍結された記録**です。大会期間中（2026年8月29日〜9月22日）に実際に
走ったスクリプトと、その成果物が入っています。

`pykeiba/` とは役割が違います。あちらは今後も直していく汎用ライブラリで、こちらは
「あの日これが動いた」という記録です。**import しないでください。** 保守しません。

## 2つのフェーズ

意思決定は時系列で明確に2つに分かれます。後から学習したモデルを過去の投票に遡って
使ったようには書きません。

| | 期間 | レース数 | 方式 |
|---|---|---:|---|
| **フェーズ1** | 8/29〜9/19 | 190 | 決定論的な方策表。**学習済み係数を読み込んでいない** |
| **フェーズ2** | 9/20 | 14 | 目標到達確率の最適化＋2係数の学習モデル（混合5%） |

## 運営要求への対応表

| 確認したいこと | 見る場所 |
|---|---|
| 意思決定の全経路（オッズ入力→送信金額） | `phase1/build_vote_plan.py:332` `build_plan` |
| 方策表の引き方 | `phase1/build_vote_plan.py:235` `lookup` |
| 方策表そのもの（31,944行） | `phase1/policy_table_v17.json` |
| 市場確率の作り方 | `phase1/build_vote_plan.py:255` `win_probabilities` |
| Harville順序確率 | `phase1/build_vote_plan.py:265` `order_probability` |
| 帯の中でどの組合せを買うか | `phase1/build_vote_plan.py:290` `pick`（価格順位95%＋確率順位5%） |
| 印の決め方 | `phase1/build_vote_plan.py:320` `marks_from_market` |
| 投票額の下限スケジュール | `phase1/build_vote_plan.py:223` `current_floor` |
| 9/20の学習コード | `phase2/optimizer.py:172` `train` |
| 学習済み係数と検証結果 | `phase2/model.json` |
| 係数が再現することの確認 | `phase2/reproduce.py` を実行 |
| 9/20の配分アルゴリズム | `phase2/optimizer.py:90` `allocate` |
| 実際に投票した204レースの買い目 | `ledger/races.json` |
| 日別収支 | `ledger/daily.json` |
| 残高照合と未解決点 | `ledger/summary.json` |

## 手元で動かす

### フェーズ1 — 実スクリプトをそのまま実行する

```bash
python3 submission/tools/synthetic_surface.py --panel data/synthetic --out /tmp/surface.json
python3 submission/phase1/build_vote_plan.py \
    --race-id <race_id> --odds /tmp/surface.json \
    --state /tmp/state.json --post-time <ISO8601> --out /tmp/plan.json
```

`/tmp/state.json` は台帳の状態です。

```json
{"races_remaining": 264, "bank": 1000000, "turnover": 0, "front_races_done": 0}
```

出力は大会中とまったく同じ形です。例:

```
202604040001  sanrentan@936.5 x3 1,000pt/点 = 3,000pt  -> /tmp/plan.json
```

**オッズが合成である以外、経路は本番と同一です。** 実際のスナップショットは収集元の
サイトに帰属するデータなので再配布できません。`submission/tools/synthetic_surface.py`
が同じマニフェスト形式を合成データから作り、実スクリプトがそれを読みます。

### フェーズ2 — 係数が再現することを確認する

```bash
python3 submission/phase2/reproduce.py
```

```
recorded_coefficients   [0.9265476143601139, 0.0]
recovered_coefficients  [0.9265475951474501, 0.0]
coefficient_difference  [1.92e-08, 0.0]
verdict                 reproduced
```

`optimizer.py` はT-10スナップショットを読むので、クローンからは実行できません。
代わりに**目的関数そのもの**を公開しています。`nll_surface.npz` は、2つの係数の格子上で
評価した「レースあたり平均NLL」を、学習用とholdout用それぞれについて持っています。
補間して最小化すれば係数が戻り、2点を読めばholdoutの数字が出ます。

レースについて平均した目的関数の値には**個別の組合せの価格が残らない**ので、これを
逆算してオッズを復元することはできません。取引は明示しておきます。**この方法が検証
するのは最適化であって、特徴量の構成ではありません。** そちらは `model.json` に
学習入力155件のSHA-256一覧があります。

## 数字を正直に読む

- holdoutのNLL改善は **5.8988 → 5.8957、0.00305 nats**。ほとんど差がありません。
- **Harville項の係数は下限0に張り付きました。** 3連単プール自身の価格が与えられている
  とき、単勝プール由来のHarville項は何も足さなかったという結果です。
- 的中は **204レース中4回（1.96%）**。分散は非常に大きく、運の寄与は大きいと考えるべきです。
- 確率は**候補の順位付けと金額計算にしか使っていません**。目標残高・穴目条件・停止条件は
  別の意思決定ルールです。「モデルが優勝を予測した」ではありません。

## 残高照合

| | |
|---|---:|
| 初期資金 | 1,000,000 |
| 投票（204レース） | −1,227,300 |
| 払戻 | +5,725,360 |
| 上記から導かれる残高 | 5,498,060 |
| 公式の最終残高 | 5,504,260 |
| **未説明** | **+6,200** |

6,200のうち5,200は取消・除外馬に対する返還として確認済みです。**残る1,000は未特定**で、
`ledger/summary.json` にそのまま記録してあります。総額に吸収していません。

もう1点。旧台帳3本は203レース・1,224,300ptでしたが、デーモンのスナップショットは
204レース・1,227,300ptです。差は9/13の `202606040404`（3,000pt）で、旧台帳がこの投票を
記録していませんでした。この公開台帳はスナップショット側を採用しています。その根拠と、
当該レースの払戻を `null` のままにしている理由は `ledger/summary.json` の
`reconciliation_notes.commentary` にあります。

## 公開にあたって変更した点

コードのロジックは1行も変えていません。変更したのは次の3点だけです。

| ファイル | 変更 | 元のSHA-256 |
|---|---|---|
| `phase1/*.py`, `phase2/optimizer.py` | 絶対パスを相対パスに。import先の名前 | — |
| `phase2/model.json` | `training_sources` の絶対パスを相対化。各ファイルのSHA-256は無変更 | `73d9e7cb26ca8a4d2ef8582338cb3c80b67e7b4cd616a4b69a4b741a5d68aa85` |
| `phase1/ranking_target_20260830.json` | `opponent_leader_name`（他参加者のハンドル名）を伏字に。コードはこの項目を読んでいません | `748c1e51e876956e33a0adbcccda2153d5de48a22de3e9aecbf89707f097ae60` |

`phase1/tail_policy.json` は `ranking_target_path` を同梱コピーへ向け直しています。
方策の値は無変更です。

同梱していないもの: 認証情報、口座情報、T-10スナップショットの実データ、収集スクリプト。

## ファイルの完全性

```bash
cd submission && shasum -a 256 -c MANIFEST.sha256
```
