# launchdでの運用

macOS専用です。GoとPythonのコード自体は移植可能ですが、この層は違いますし、
そのふりもしません。

## そもそもなぜスーパーバイザが要るのか

レースの送信ウィンドウは数分幅で一度きり、二度と来ません。そのウィンドウの中で
プロセスが死に、再起動するものが何もなければ、そのレースは単に落ちます。`KeepAlive`
はクラッシュを数秒のダウンタイムに変え、[02-voting-runtime.md](02-voting-runtime.md)
の復帰規則が再起動を安全にします——送信途中で見つかったレースは、再送ではなく相手側を
読むことで決着します。

## 導入

```bash
./scripts/launchd.sh install --policy var/policy_20260926.json --driver paper
./scripts/launchd.sh status
./scripts/launchd.sh logs
./scripts/launchd.sh uninstall
```

スクリプトは `launchd/templates/votingd.plist.template` を
`~/Library/LaunchAgents/` へ展開し、プロジェクトの絶対パス、ポリシーのパス、ドライバ、
タイムゾーンを埋めます。読み込む前に `plutil -lint` を通し、`launchctl bootout`
（失敗は無視）してから `launchctl bootstrap` します。

`--driver live` は導入前にタイプ入力による確認を求めます。これは意図的な摩擦です。

複数走らせる場合は `LABEL_PREFIX=...` でラベル接頭辞を変えてください。

## 明示しない限りエージェントは送信できない

launchdエージェントはシェルからほとんど何も継承しないので、ポリシーが要求する
日付スコープの変数（`KEIBA_ENABLE_SUBMISSION`）は単に存在しません。導入して忘れられた
エージェントは、起動し、状態を提供し、そして**当日の武装を拒否します**。これが意図した
既定です。

```bash
./scripts/launchd.sh install --policy var/policy_20260926.json --enable-submission
```

`--enable-submission` はポリシーの `required_environment` をplistへ複写し、何を
設定したかを表示します。値は認証情報ではなく日付です——運用者が「今日」と言っている
のと同じことで、複写元のポリシーはまさにその1日だけ有効です。別の日に導入し直すには、
別のポリシーと、もう一度の明示的なフラグが必要です。

## 認証情報はplistに置かない

plistは誰でも読めますし、バックアップにも入ります。テンプレートが設定するのは `TZ`
だけです。

紙投票ドライバは認証情報を一切必要としません。何も認証しないので、要求してもlaunchd
エージェントが届きもしないログインで失敗するだけです。

実ドライバは `KEIBA_LOGIN_ID` と `KEIBA_PASSWORD` を環境から読みます。macOSでの
妥当な置き場所はKeychainです。

```bash
security add-generic-password -U -a "$(id -un)" -s local.keiba.login-id  -w
security add-generic-password -U -a "$(id -un)" -s local.keiba.password  -w
```

そのうえで、`EnvironmentVariables` に書くのではなく、それらをexportするラッパーから
デーモンを起動してください。

## スリープするとレースを落とす

これが最も重要な失敗モードで、launchdはこれを解決しません。**眠っているMacは何も
実行しません。** `ProcessType Interactive` はApp Napによるスロットリングを避ける
よう頼むだけで、マシンを起こし続けはしません。

スケジュールに沿って送信するつもりなら、そのウィンドウのあいだ電源アサーションを
保持してください。

```bash
caffeinate -i -w $(pgrep -f 'bin/votingd')
```

あるいは電源接続時に `caffeinate -s`。実際に何かがマシンを起こし続けているかは
`pmset -g assertions` で確認できます。ノートのふたを閉じれば、何をしていても
スリープします。

## デーモンがすぐクラッシュループする場合

まず `.err.log` と `.out.log` を見てください。ひとつ、原因が誤解を招くので名前を
挙げておく価値のあるものがあります。macOSではUnixソケットのパスが104バイトに制限
されており、深いディレクトリにcloneすると制御ソケットがそれを超えます。`bind()` は
`EINVAL` を返し、「invalid argument」という表示になるので、権限の問題に見えます。

現在のデーモンは、長さと制限を明示したメッセージで拒否します。対処は、チェックアウト
をもっと短い場所へ移すか、ポリシーの `interfaces.control_socket` を短い絶対パスへ
向けることです。

## ログ

`var/log/<label>.out.log` と `.err.log`。デーモンは構造化JSONを1行ずつ書くので、

```bash
tail -f var/log/local.keiba-masters-kit.votingd.out.log | jq .
```

launchdはこれらをローテートしません。自分でローテートするか、日をまたぐ際に切り詰めて
ください。

## 本当に動いているかの確認

`launchctl print gui/$(id -u)/<label>` でPID、直近の終了状態、再起動回数が見えます。
PIDがないまま再起動回数だけ増えていれば、デーモンはクラッシュループしています。
その場合に見るべきは `launchctl` ではなく `.err.log` です。
