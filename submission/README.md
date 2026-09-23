# 大会で実際に使ったコード

このディレクトリには、AI競馬予想マスターズ2026の期間中（2026年8月29日〜9月22日）に実際に動かしたスクリプトと、その結果を、当時の状態のまま収めています。運営への提出物として整理したものです。

リポジトリの `pykeiba/` とは役割が異なります。`pykeiba/` は今後も手を入れていく汎用のライブラリで、こちらは当時の記録です。他のコードから読み込んで使うことは想定しておらず、今後の修正も行いません。

## 各レースの投票方針

大会期間中、投票方針は日によって変えています。どの方針でどのレースに投票したかは次のとおりです。

| 期間 | レース数 | 方針（policy_id） | 券種・価格帯・金額の決め方 | スクリプト |
|---|---:|---|---|---|
| 8/29 | 29 | `V17` | 方策表を参照（残りレース数と所持ポイントから決定） | `phase1/build_vote_plan.py` |
| 8/30 | 12 | `V18-TAIL133` | 馬単133倍付近を1点、金額は目標残高から逆算 | `phase1/build_vote_plan.py` |
| 8/30、9/5〜9/13 | 139 | `V20-SANRENTAN3-1000` | 三連単936.5倍付近を3点、1点1,000pt（全レース共通） | `phase1/sanrentan3_plan.py` |
| 9/19 | 10 | `ALLIN-*` | 375〜8000倍の組合せから、レースの区分ごとに配分 | `phase1/allin_plan.py`、`phase1/recover_allin.py` |
| 9/20 | 14 | `DYNAMIC-20260920` | 目標残高から逆算し、学習済みモデルを5%混ぜて配分 | `phase2/optimizer.py` |

方針ごとのレース数・投票額・払戻額は、`ledger/summary.json` の `by_policy` と `decision_mechanism` にも記録しています。

### 方策表と V20 の関係

8/29 は、方策表（`phase1/policy_table_v17.json`、31,944行）をレースごとに参照して投票しました。

8/30 以降の V20 は、このとき方策表が示した行動の1つ（三連単936.5倍付近を3点、1点1,000pt）を固定し、以後は状態によらず各レースに適用したものです。V20 の設定ファイル（`phase1/sanrentan3_policy.json`）には、その元になった方策と計画が記録されています。

```json
"source_policy_id": "COMPETITION-2026-V17",
"source_evidence_plan": "outputs/competition-2026/plans/2026082901020306.json",
"status": "USER_OVERRIDDEN_ALL_RACE_POLICY"
```

どの方針でも、選んだ価格帯の中で具体的にどの組合せを買うかは同じ方法で決めています（`phase1/build_vote_plan.py:290` の `pick`。価格の近さ95%、Harville 確率の順位5%）。

### 学習済みモデルを使ったのは9/20の14レースのみ

8/29〜9/19 のスクリプトは、学習済みの重みを読み込んでいません。学習済みモデル（`phase2/model.json`）を使ったのは、9/20 の14レースだけです。

## 確認したい内容と、その場所

| 確認したい内容 | 場所 |
|---|---|
| オッズの入力から送信金額までの処理の流れ | `phase1/build_vote_plan.py:332` `build_plan` |
| 方策表の参照方法 | `phase1/build_vote_plan.py:235` `lookup` |
| 方策表そのもの | `phase1/policy_table_v17.json` |
| 市場確率の求め方 | `phase1/build_vote_plan.py:255` `win_probabilities` |
| Harville 順序確率 | `phase1/build_vote_plan.py:265` `order_probability` |
| 価格帯の中での組合せの選び方 | `phase1/build_vote_plan.py:290` `pick` |
| 印の付け方 | `phase1/build_vote_plan.py:320` `marks_from_market` |
| 投票額の下限の決め方 | `phase1/build_vote_plan.py:223` `current_floor` |
| V20（139レース）の方針 | `phase1/sanrentan3_plan.py`、`phase1/sanrentan3_policy.json` |
| 9/19 の組合せの絞り込みと配分 | `phase1/allin_plan.py:77` ほか |
| 9/20 の学習 | `phase2/optimizer.py:172` `train` |
| 9/20 の配分 | `phase2/optimizer.py:90` `allocate` |
| 学習済みの重みと検証結果 | `phase2/model.json` |
| 重みが再現できることの確認 | `phase2/reproduce.py` |
| 204レースの買い目 | `ledger/races.json` |
| 日ごとの収支 | `ledger/daily.json` |
| 残高の照合 | `ledger/summary.json` |

## 手元で動かす

### 8/29〜9/19 のスクリプトを実行する

実際のオッズは提供元に権利があるため同梱していません。代わりに、合成データから同じ形式のオッズファイルを作って実行できるようにしています。

```bash
python3 submission/tools/synthetic_surface.py --panel data/synthetic --out /tmp/surface.json
python3 submission/phase1/build_vote_plan.py \
    --race-id <レースID> --odds /tmp/surface.json \
    --state /tmp/state.json --post-time <発走時刻（ISO 8601）> --out /tmp/plan.json
```

`/tmp/state.json` には、その時点の所持ポイントなどを書きます。

```json
{"races_remaining": 264, "bank": 1000000, "turnover": 0, "front_races_done": 0}
```

オッズが合成であることを除けば、大会中と同じ処理を通り、同じ形式の結果が出力されます。

```
202604040001  sanrentan@936.5 x3 1,000pt/点 = 3,000pt  -> /tmp/plan.json
```

### 9/20 のモデルの重みを再現する

```bash
python3 submission/phase2/reproduce.py
```

```
recorded_coefficients   [0.9265476143601139, 0.0]
recovered_coefficients  [0.9265475951474501, 0.0]
coefficient_difference  [1.92e-08, 0.0]
verdict                 reproduced
```

学習に使ったオッズは同梱できないため、`phase2/optimizer.py` をそのまま実行することはできません。代わりに、学習で最小化した関数そのものの値を公開しています。`nll_surface.npz` は、2つの重みを細かく変えたときの「レースあたりの平均 NLL」を、学習用と評価用のデータそれぞれについて記録したものです。これをなめらかにつないで最小値を探すと、記録されている重みが再現されます。

この値はレース全体の平均なので、個々の組合せのオッズは含まれておらず、ここからオッズを復元することはできません。一方で、この方法で確かめられるのは最適化の部分であり、特徴量が正しく作られていたかまでは確かめられません。その点については、`model.json` に学習に使った155レース分の入力ファイルの SHA-256 を記録しています。

## 結果について

- 9/20 のモデルによる評価用データでの NLL の改善は、5.8988 から 5.8957 へ、0.00305 とごくわずかです。
- Harville の重みは下限の0になりました。三連単のオッズがすでにある場合、単勝オッズから求めた確率は新しい情報を加えなかった、という結果です。
- 的中は204レース中4回（1.96%）でした。結果のばらつきは非常に大きく、運による部分が大きかったと考えています。
- 確率は、候補の順位付けと金額の計算にだけ使っています。目標残高、組合せの絞り込み条件、投票を止める条件は、それとは別に定めたルールです。

## 残高の照合

| | ポイント |
|---|---:|
| 初期資金 | 1,000,000 |
| 投票（204レース） | −1,227,300 |
| 払戻 | +5,725,360 |
| 上記から計算した残高 | 5,498,060 |
| 大会の最終残高 | 5,504,260 |
| 差額 | +6,200 |

差額 6,200 のうち 5,200 は、出走取消・競走除外による返還であることを確認しています。残る 1,000 は内訳を特定できておらず、その旨を `ledger/summary.json` に記録しています。

なお、以前の台帳（3ファイル）には203レース・1,224,300pt が記録されていましたが、投票デーモンの記録（スナップショット）では204レース・1,227,300pt です。差は 9/13 のレース `202606040404`（3,000pt）で、以前の台帳に記録漏れがありました。本台帳ではデーモンの記録を採用しています。このレースの払戻を `null` としている理由は、`ledger/summary.json` の `reconciliation_notes.commentary` に記載しています。

## 公開にあたって変更した点

プログラムの処理内容は変更していません。変更したのは次の点だけです。

| ファイル | 変更内容 | 変更前の SHA-256 |
|---|---|---|
| `phase1/*.py`、`phase2/optimizer.py` | ファイルの場所を示すパスを相対パスに変更し、読み込むファイル名を調整 | — |
| `phase2/model.json` | `training_sources` 内のパスを相対パスに変更（各入力ファイルの SHA-256 は変更なし） | `73d9e7cb26ca8a4d2ef8582338cb3c80b67e7b4cd616a4b69a4b741a5d68aa85` |
| `phase1/ranking_target_20260830.json` | 他の参加者のユーザー名を伏せ字に変更（プログラムはこの項目を使っていません） | `748c1e51e876956e33a0adbcccda2153d5de48a22de3e9aecbf89707f097ae60` |

`phase1/tail_policy.json` は、参照先を同梱した `ranking_target_20260830.json` に変更しています。方針の値は変更していません。

認証情報、アカウント情報、学習に使った実際のオッズ、データの収集に使ったプログラムは含めていません。

## 公開について

本ディレクトリの公開にあたっては、大会終了後に運営の皆さまへ確認し、ご了承をいただきました。ご対応いただいたことに感謝申し上げます。

## ファイルの確認

同梱したファイルが改変されていないことは、次のコマンドで確認できます。

```bash
cd submission && shasum -a 256 -c MANIFEST.sha256
```
