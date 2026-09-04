#!/usr/bin/env python3
"""OpenAI互換エンドポイントの decode 速度を測る。

prefill と decode を分けるためストリーミングで受け、
最初のトークンが来るまでを TTFT、それ以降を decode 時間として扱う。
サーバー間で比べるので、プロンプト・生成長・温度は呼び出し側で固定すること。
"""
import argparse
import json
import os
import statistics
import sys
import time
import urllib.request

PROMPT = (
    "次のシェルコマンドについて、日本語で詳しく説明してください。"
    "各オプションの意味、想定される出力、実行時の注意点、"
    "似た用途の別コマンドとの違いを順に述べてください。\n\n"
    "git log --oneline --graph --decorate --all"
)


def one_run(base, key, model, max_tokens, temp):
    body = json.dumps({
        "model": model,
        "messages": [{"role": "user", "content": PROMPT}],
        "max_tokens": max_tokens,
        "temperature": temp,
        "stream": True,
        "stream_options": {"include_usage": True},
    }).encode()

    req = urllib.request.Request(
        base.rstrip("/") + "/chat/completions", data=body,
        headers={"Content-Type": "application/json",
                 **({"Authorization": "Bearer " + key} if key else {})})

    t0 = time.perf_counter()
    ttft = None
    n = 0
    usage_completion = None

    with urllib.request.urlopen(req, timeout=600) as resp:
        for raw in resp:
            line = raw.decode("utf-8", "replace").strip()
            if not line.startswith("data:"):
                continue
            payload = line[5:].strip()
            if payload == "[DONE]":
                break
            try:
                ev = json.loads(payload)
            except json.JSONDecodeError:
                continue
            if ev.get("usage"):
                usage_completion = ev["usage"].get("completion_tokens")
            for ch in ev.get("choices") or []:
                delta = ch.get("delta") or {}
                # Qwen3.8 は思考中のトークンを reasoning_content で出す。
                # どちらも生成トークンなので decode 速度としては同じく数える。
                piece = delta.get("content") or delta.get("reasoning_content")
                if piece:
                    if ttft is None:
                        ttft = time.perf_counter() - t0
                    n += 1
    total = time.perf_counter() - t0

    if ttft is None or n < 2:
        raise RuntimeError(f"トークンが取れませんでした (n={n})")

    # usage が返るならそちらが正確。チャンク数はトークン数と一致しないことがある。
    tokens = usage_completion or n
    decode_s = total - ttft
    return {
        "ttft": ttft,
        "total": total,
        "tokens": tokens,
        "chunks": n,
        "decode_tok_s": (tokens - 1) / decode_s if decode_s > 0 else 0.0,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", required=True)
    ap.add_argument("--label", required=True)
    ap.add_argument("--model", default="")
    ap.add_argument("--runs", type=int, default=3)
    ap.add_argument("--max-tokens", type=int, default=512)
    ap.add_argument("--temp", type=float, default=0.0)
    args = ap.parse_args()

    key = os.environ.get("BENCH_API_KEY", "")
    model = args.model
    if not model:
        req = urllib.request.Request(
            args.base.rstrip("/") + "/models",
            headers={"Authorization": "Bearer " + key} if key else {})
        with urllib.request.urlopen(req, timeout=30) as r:
            model = json.load(r)["data"][0]["id"]

    print(f"[{args.label}] base={args.base} model={model} "
          f"max_tokens={args.max_tokens} temp={args.temp}", flush=True)

    print("  warmup...", flush=True)
    one_run(args.base, key, model, 64, args.temp)

    rows = []
    for i in range(args.runs):
        r = one_run(args.base, key, model, args.max_tokens, args.temp)
        rows.append(r)
        print(f"  run{i+1}: decode {r['decode_tok_s']:6.2f} tok/s  "
              f"TTFT {r['ttft']*1000:7.1f} ms  "
              f"tokens {r['tokens']:4d}  total {r['total']:6.2f}s", flush=True)

    dec = [r["decode_tok_s"] for r in rows]
    ttft = [r["ttft"] for r in rows]
    out = {
        "label": args.label, "model": model, "base": args.base,
        "max_tokens": args.max_tokens, "temp": args.temp, "runs": args.runs,
        "decode_median": statistics.median(dec),
        "decode_mean": statistics.fmean(dec),
        "decode_min": min(dec), "decode_max": max(dec),
        "ttft_median_ms": statistics.median(ttft) * 1000,
        "tokens_median": statistics.median(r["tokens"] for r in rows),
    }
    print(f"  => decode 中央値 {out['decode_median']:.2f} tok/s "
          f"(min {out['decode_min']:.2f} / max {out['decode_max']:.2f}), "
          f"TTFT 中央値 {out['ttft_median_ms']:.1f} ms", flush=True)

    # Windows 側からも走らせるので、スクリプトと同じ場所に置く。
    dest = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                        f"bench-{args.label}.json")
    with open(dest, "w") as f:
        json.dump(out, f, ensure_ascii=False, indent=2)
    return 0


if __name__ == "__main__":
    sys.exit(main())
