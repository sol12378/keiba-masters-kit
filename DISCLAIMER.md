# Disclaimer

**Read this before running anything with `--driver live`.**

## This software operates on contest points

It was written for the AI 競馬予想マスターズ 2026 contest, which is scored in
virtual points. Nothing in this repository buys a real betting ticket, and no
part of it is a wagering interface for real money.

## It does not give you an edge

The model here has two market-derived features. The dynamic program assumes
every price band pays back less than it takes in, because that is what
pari-mutuel pools do. Across any run the expected final bank is **below** the
starting bank; the verification step prints that number so it cannot be missed.

A strategy that maximizes the chance of reaching a target does so by accepting
a very high chance of losing most of the bank. In the shipped configuration the
median outcome is near zero. If you adapt this to real money, that median is
what you should expect.

## It is not advice

Nothing here is investment, financial or betting advice. No warranty is given,
express or implied, including fitness for any particular purpose. You are
responsible for what you run and for any losses.

## Legal and account use

* Betting on horse racing is restricted to adults (20 and over in Japan) and is
  illegal in many jurisdictions. Complying with the law where you are is your
  responsibility.
* Automating access to any service is governed by that service's terms. Read
  them. Using the live driver against an account is your own act, under your
  own agreement with that service.
* If gambling is causing harm to you or someone you know, support is available.
  In Japan: 全国ギャンブル依存症家族の会, and the Ministry of Health, Labour and
  Welfare's 依存症相談拠点 directory.
