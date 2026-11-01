# Changelog

## [v0.2.1](https://github.com/yteraoka/check_http_go/compare/v0.2...v0.2.1) - 2026-11-01

- Configure Renovate by @renovate[bot] in https://github.com/yteraoka/check_http_go/pull/7
- fix: format response time to milliseconds by @Vamsi-2581 in https://github.com/yteraoka/check_http_go/pull/22
- go.mod / go.sum を追加してモジュールモードでビルドできるようにする by @yteraoka in https://github.com/yteraoka/check_http_go/pull/23
- テスト・CI・GoReleaser によるリリース自動化を追加する by @yteraoka in https://github.com/yteraoka/check_http_go/pull/24
- -S と -p を同時に指定したときに HTTPS で接続するように修正 by @yteraoka in https://github.com/yteraoka/check_http_go/pull/26
- -v の出力を stderr に変更し、Nagios のステータス行が壊れないようにする by @yteraoka in https://github.com/yteraoka/check_http_go/pull/27
- エラー時の終了コードとメッセージを Nagios プラグインの規約と check_http に合わせる by @yteraoka in https://github.com/yteraoka/check_http_go/pull/28
- JSON チェックの値の比較とエラー処理を修正 by @yteraoka in https://github.com/yteraoka/check_http_go/pull/29
- -H の値を SNI として送信し、証明書を検証する --verify-cert / --ca-cert を追加 by @yteraoka in https://github.com/yteraoka/check_http_go/pull/30
- 失敗理由を 1 行目に出力し、perfdata に閾値を含める by @yteraoka in https://github.com/yteraoka/check_http_go/pull/31
- IPv6 アドレス、ポート付きの -H、/ で始まらない -u で URL を正しく組み立てる by @yteraoka in https://github.com/yteraoka/check_http_go/pull/32
- リファクタリング: main() の分割、deprecated API の置き換え、命名の整理 by @yteraoka in https://github.com/yteraoka/check_http_go/pull/33
- check_http 互換オプションを追加 (-s, -r/-R, -m, -a, -P/-T, -f, -4/-6, -C) by @yteraoka in https://github.com/yteraoka/check_http_go/pull/34
- -k で ": " 以外の書式を受け付け、不正なヘッダを UNKNOWN にする by @yteraoka in https://github.com/yteraoka/check_http_go/pull/35
- Release with tagpr by @yteraoka in https://github.com/yteraoka/check_http_go/pull/36

## [](https://github.com/yteraoka/check_http_go/compare/v0.1...v0.2) - 2018-01-30

- support `Host` header by @yteraoka in https://github.com/yteraoka/check_http_go/pull/2
- support additional headers (`-k` option) by @yteraoka in https://github.com/yteraoka/check_http_go/pull/3
- go fmt by @yteraoka in https://github.com/yteraoka/check_http_go/pull/4
- Bump version by @yteraoka in https://github.com/yteraoka/check_http_go/pull/5
- version 0.2 by @yteraoka in https://github.com/yteraoka/check_http_go/pull/6

## [](https://github.com/yteraoka/check_http_go/commits/v0.1) - 2018-01-21

- first release v0.1 by @yteraoka in https://github.com/yteraoka/check_http_go/pull/1
