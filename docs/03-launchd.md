# launchd での常駐運用

この文書の内容は macOS 専用です。Go と Python のコードは他の OS でも動くはずですが、常駐の仕組みは OS ごとに違い、私は macOS でしか運用していないため、それ以外の手順は用意していません。

## 常駐させた理由

レースごとの送信のタイミングは数分の幅しかなく、逃すと取り戻せません。その間にプロセスが止まり、誰も再起動しなければ、そのレースには投票できないままになります。launchd の自動再起動（`KeepAlive`）を使えば、プロセスが止まっても数秒で再開できます。再開したデーモンは送信途中のレースを再送信せず、大会側の記録を読み返して状態を確定させるので（[02-voting-runtime.md](02-voting-runtime.md) 参照）、自動再起動しても二重投票にはなりません。

## 導入と確認

```bash
./scripts/launchd.sh install --policy var/policy_20260926.json --driver paper
./scripts/launchd.sh status
./scripts/launchd.sh logs
./scripts/launchd.sh uninstall
```

`install` は `launchd/templates/votingd.plist.template` をもとに、プロジェクトの場所・ポリシー・ドライバ・タイムゾーンを埋め込んだ設定ファイルを `~/Library/LaunchAgents/` に作ります。作った設定は `plutil -lint` で確認してから読み込みます。

`--driver live`（本番）を指定した場合は、導入の前に確認の入力を求めます。誤って本番用に導入しないための手順です。

複数のデーモンを動かす場合は、環境変数 `LABEL_PREFIX` でラベルの接頭辞を変えてください。

## 送信を許可するには明示的な指定が必要

launchd で起動したプロセスは、ターミナルの環境変数を引き継ぎません。そのため、ポリシーが送信の条件として求める日付入りの環境変数（`KEIBA_ENABLE_SUBMISSION`）も設定されず、導入しただけではその日の送信を許可できません。導入したまま忘れたデーモンが勝手に送信しないよう、これを既定の動作にしました。

実際に送信するつもりのときは、次のように指定します。

```bash
./scripts/launchd.sh install --policy var/policy_20260926.json --enable-submission
```

`--enable-submission` は、ポリシーに書かれた環境変数の指定を設定ファイルに書き写し、何を設定したかを表示します。書き写す値は認証情報ではなく日付で、ポリシー自体もその1日だけ有効です。別の日に使うときは、その日のポリシーで改めて導入し直す必要があります。

## 認証情報は設定ファイルに書かない

launchd の設定ファイルは他のユーザーからも読めるうえ、バックアップにも含まれます。そのため、テンプレートで設定する環境変数はタイムゾーンだけにしています。

模擬投票ドライバは認証を行わないので、認証情報は必要ありません。

本番ドライバは `KEIBA_LOGIN_ID` と `KEIBA_PASSWORD` を環境変数から読み込みます。macOS ではキーチェーンに保存しておき、

```bash
security add-generic-password -U -a "$(id -un)" -s local.keiba.login-id -w
security add-generic-password -U -a "$(id -un)" -s local.keiba.password -w
```

それを環境変数に読み込んでからデーモンを起動する小さなスクリプトを用意するのが安全です。設定ファイルの `EnvironmentVariables` には書かないでください。

## スリープ中は投票できません

これは一番起こりやすく、しかも launchd では防げない失敗です。スリープ中の Mac では何も動きません。設定ファイルの `ProcessType Interactive` は省電力機能による処理の遅延を避けるためのもので、スリープそのものは防ぎません。

投票を予定している時間帯は、スリープしないようにしておいてください。

```bash
caffeinate -i -w $(pgrep -f 'bin/votingd')
```

電源に接続している場合は `caffeinate -s` も使えます。実際にスリープが抑止されているかは `pmset -g assertions` で確認できます。ノート型の Mac は、ふたを閉じるとこれらの設定にかかわらずスリープします。

## すぐに再起動を繰り返す場合

まず `var/log/` の `.err.log` と `.out.log` を確認してください。

原因のひとつに、気づきにくいものがあります。macOS では Unix ソケットのパスが104バイトまでに制限されており、深い階層にリポジトリを置くと、操作用ソケットのパスがこれを超えることがあります。このときの OS のエラーは「invalid argument」という表示になり、権限の問題のように見えてしまいます。

現在のデーモンは、パスの長さと上限を示すメッセージを出して停止します。リポジトリをもっと浅い場所に移すか、ポリシーの `interfaces.control_socket` に短い絶対パスを指定してください。

## ログ

`var/log/<ラベル>.out.log` と `.err.log` に出力されます。デーモンは1行ずつ JSON 形式で記録するので、次のように整形して読めます。

```bash
tail -f var/log/local.keiba-masters-kit.votingd.out.log | jq .
```

launchd はログを自動では整理しません。必要に応じてご自身で削除や切り詰めを行ってください。

## 動いているかの確認

`launchctl print gui/$(id -u)/<ラベル>` で、プロセスID、最後の終了状態、再起動の回数を確認できます。プロセスIDがないまま再起動の回数だけが増えている場合は、起動と停止を繰り返しています。そのときは `launchctl` の表示よりも `.err.log` を確認してください。
