"""配乐探针（P2）：在 NAS 上不用 allin1，能不能找出节拍、重拍与副歌。

剪辑中心方案（docs/architecture/montage-center.md 的“配乐”）要在设备上分析成员自己的歌：
- 节拍与重拍：beat_this 的 small 模型导出成 ONNX，用设备已有的 onnxruntime 跑；梅尔谱、
  分块推理与后处理用 NumPy 自己写，不带 PyTorch。
- 副歌：按小节对齐的色度自相似找重复段，再按响度挑，最后套第六版“下一段更响就是铺垫”的规则。

三个子命令，结果写进 docs/research/montage-music-analysis.md：
- export：从上游检查点导出 ONNX（要 PyTorch 与 beat_this，只在探针的虚拟环境里有）；
- check：同一份采样，NumPy 加 onnxruntime 的结果与上游 PyTorch 逐帧对照；
- songs：10 首测试歌对照 allin1 的节拍与重拍、人工确认的副歌，并计时。

设备端只用到 numpy 与 onnxruntime；torch、beat_this、mir_eval 只在 export、check 与 songs 里导入。
"""

import argparse
import itertools
import json
import resource
import shutil
import subprocess
import sys
import time
from pathlib import Path

import numpy as np

RATE = 22050
N_FFT = 1024
HOP = 441  # 50 帧每秒
FFMPEG = shutil.which("ffmpeg") or "/usr/bin/ffmpeg"
FPS = RATE / HOP
CHUNK = 1500
CENTER = True  # 小节特征减去全曲平均（songs 的 --raw 关掉，用来对照）
BORDER = 6


def decode(path):
    """单声道 22050 Hz 的 float32 采样，与 FFmpeg Worker 的 `audio` 输出相同。"""
    raw = subprocess.run(
        [FFMPEG, "-v", "error", "-i", str(path), "-ac", "1", "-ar", str(RATE), "-f", "f32le", "-"],
        check=True,
        capture_output=True,
    ).stdout
    return np.frombuffer(raw, dtype=np.float32)


def _hz_to_mel(f):
    # Slaney 刻度：1000 Hz 以下线性，以上对数（与 torchaudio 的 mel_scale="slaney" 相同）。
    f = np.asarray(f, dtype=np.float64)
    mel = 3.0 * f / 200.0
    log = f >= 1000.0
    return np.where(log, 15.0 + np.log(np.maximum(f, 1e-10) / 1000.0) / (np.log(6.4) / 27.0), mel)


def _mel_to_hz(m):
    m = np.asarray(m, dtype=np.float64)
    f = 200.0 * m / 3.0
    log = m >= 15.0
    return np.where(log, 1000.0 * np.exp((np.log(6.4) / 27.0) * (m - 15.0)), f)


def mel_filters(n_freqs=N_FFT // 2 + 1, f_min=30.0, f_max=11000.0, n_mels=128):
    """torchaudio.functional.melscale_fbanks(norm=None, mel_scale="slaney") 的 NumPy 版，形状 (n_freqs, n_mels)。"""
    all_freqs = np.linspace(0, RATE // 2, n_freqs)
    m_pts = np.linspace(_hz_to_mel(f_min), _hz_to_mel(f_max), n_mels + 2)
    f_pts = _mel_to_hz(m_pts)
    f_diff = f_pts[1:] - f_pts[:-1]
    slopes = f_pts[None, :] - all_freqs[:, None]
    down = -slopes[:, :-2] / f_diff[:-1]
    up = slopes[:, 2:] / f_diff[1:]
    return np.maximum(0.0, np.minimum(down, up)).astype(np.float32)


def stft_magnitude(x):
    """torchaudio.Spectrogram(n_fft=1024, hop=441, power=1, normalized="frame_length") 的 NumPy 版：(帧, 513)。"""
    window = 0.5 - 0.5 * np.cos(2 * np.pi * np.arange(N_FFT) / N_FFT)  # 周期 Hann 窗
    pad = N_FFT // 2
    padded = np.pad(x.astype(np.float32), (pad, pad), mode="reflect")
    frames = 1 + (len(padded) - N_FFT) // HOP
    idx = np.arange(N_FFT)[None, :] + HOP * np.arange(frames)[:, None]
    spec = np.abs(np.fft.rfft(padded[idx] * window.astype(np.float32), axis=1))
    # normalized="frame_length" 交给 torch.stft(normalized=True)：除以 √n_fft，不是除以窗的能量。
    return (spec / np.sqrt(N_FFT)).astype(np.float32)


def log_mel(x, filters=None):
    """beat_this 的 LogMelSpect：log1p(1000 · 梅尔幅度)，形状 (帧, 128)。"""
    filters = mel_filters() if filters is None else filters
    return np.log1p(1000.0 * (stft_magnitude(x) @ filters))


def split_piece(spect):
    """beat_this.inference.split_piece(chunk_size=1500, border_size=6, avoid_short_end=True)。"""
    n = len(spect)
    starts = list(range(-BORDER, n - BORDER, CHUNK - 2 * BORDER))
    if n > CHUNK - 2 * BORDER:
        starts[-1] = n - (CHUNK - BORDER)
    chunks = []
    for s in starts:
        part = spect[max(s, 0) : min(s + CHUNK, n)]
        left = max(0, -s)
        right = max(0, min(BORDER, s + CHUNK - n))
        chunks.append(np.pad(part, ((left, right), (0, 0))))
    return chunks, starts


def frame_logits(session, spect):
    """分块跑 ONNX 再拼回整首，重叠处保留前一块（keep_first）。"""
    chunks, starts = split_piece(spect)
    beat = np.full(len(spect), -1000.0, dtype=np.float32)
    down = np.full(len(spect), -1000.0, dtype=np.float32)
    for s, chunk in reversed(list(zip(starts, chunks, strict=True))):
        b, d = session.run(None, {"spect": chunk[None].astype(np.float32)})
        b, d = b[0][BORDER:-BORDER], d[0][BORDER:-BORDER]
        lo, hi = s + BORDER, s + CHUNK - BORDER
        keep = slice(max(lo, 0), min(hi, len(spect)))
        beat[keep] = b[keep.start - lo : keep.stop - lo]
        down[keep] = d[keep.start - lo : keep.stop - lo]
    return beat, down


def _dedupe(frames):
    out, it = [], iter(int(f) for f in frames)
    try:
        p, c = float(next(it)), 1
    except StopIteration:
        return np.array([])
    for f in it:
        if f - p <= 1:
            c += 1
            p += (f - p) / c
        else:
            out.append(p)
            p, c = float(f), 1
    out.append(p)
    return np.array(out)


def peaks(beat_logits, down_logits):
    """beat_this 的 minimal 后处理：7 帧内的极大值且 logit > 0，去重，重拍挪到最近的节拍。"""

    def pick(logits):
        padded = np.pad(logits, 3, constant_values=-np.inf)
        window = np.lib.stride_tricks.sliding_window_view(padded, 7).max(axis=1)
        return np.nonzero((logits == window) & (logits > 0))[0]

    beats = _dedupe(pick(beat_logits)) / 50.0
    downs = _dedupe(pick(down_logits)) / 50.0
    if len(beats):
        downs = beats[np.abs(beats[None, :] - downs[:, None]).argmin(axis=1)] if len(downs) else downs
    return beats, np.unique(downs)


def beats_onnx(session, x):
    spect = log_mel(x)
    return peaks(*frame_logits(session, spect))


# ---------------------------------------------------------------- 副歌


def chroma(mag):
    """每帧 12 个音级的能量，取 55–5000 Hz，对数压缩后按帧归一化。"""
    freqs = np.linspace(0, RATE / 2, mag.shape[1])
    keep = (freqs >= 55) & (freqs <= 5000)
    pitch = np.round(12 * np.log2(freqs[keep] / 440.0) + 69).astype(int) % 12
    out = np.zeros((mag.shape[0], 12), dtype=np.float32)
    for k in range(12):
        out[:, k] = np.log1p(100 * mag[:, keep][:, pitch == k]).sum(axis=1)
    return out / (np.linalg.norm(out, axis=1, keepdims=True) + 1e-9)


def bars_from(beats, downs, duration):
    """小节：相邻两个重拍之间；首个重拍之前、最后一个之后的零头不算。"""
    downs = np.asarray(downs)
    downs = downs[downs < duration]
    return [(a, b) for a, b in itertools.pairwise(downs) if b - a > 0.5]


def bar_features(x, beats, bars):
    mag = stft_magnitude(x)
    ch = chroma(mag)
    rms = np.sqrt(np.mean(x[: len(x) // HOP * HOP].reshape(-1, HOP) ** 2, axis=1) + 1e-12)
    feats, loud = [], []
    for a, b in bars:
        inner = [t for t in beats if a - 0.02 <= t < b - 0.02]
        edges = [*inner, b] if inner else [a, b]
        parts = []
        for k in range(4):  # 每小节按拍分 4 份；不是 4 拍时按时长均分
            if len(edges) == 5:
                s, e = edges[k], edges[k + 1]
            else:
                s, e = a + (b - a) * k / 4, a + (b - a) * (k + 1) / 4
            fs, fe = int(s * FPS), max(int(e * FPS), int(s * FPS) + 1)
            parts.append(ch[fs:fe].mean(axis=0))
        v = np.concatenate(parts)
        feats.append(v / (np.linalg.norm(v) + 1e-9))
        fs, fe = int(a * FPS), max(int(b * FPS), int(a * FPS) + 1)
        loud.append(20 * np.log10(rms[fs:fe].mean() + 1e-12))
    feats = np.array(feats)
    if CENTER:
        # 色度全是正数，任两小节的余弦都很高；减去全曲平均再归一化，相似度才分得开。
        feats = feats - feats.mean(axis=0)
        feats /= np.linalg.norm(feats, axis=1, keepdims=True) + 1e-9
    return feats, np.array(loud)


def repeated_blocks(ssm, length=8, threshold=0.8):
    """每个起点小节开始的 length 小节，在歌里还有几处相似（对角线平均相似度 ≥ threshold，不重叠）。"""
    n = len(ssm)
    score = np.zeros(n)
    for s in range(n - length + 1):
        hits = 0
        last = -length
        for t in range(n - length + 1):
            if abs(t - s) < length or t - last < length:
                continue
            if np.mean([ssm[s + i, t + i] for i in range(length)]) >= threshold:
                hits += 1
                last = t
        score[s] = hits
    return score


def relative(ssm, near=4):
    """把相似度换成在这首歌里的百分位：有的歌处处相似（Fortnight），绝对门槛分不开。"""
    n = len(ssm)
    off = np.abs(np.arange(n)[:, None] - np.arange(n)[None, :]) >= near
    values = np.sort(ssm[off])
    return np.searchsorted(values, ssm) / max(len(values), 1)


def find_chorus_v2(x, beats, downs, length=8, rise_db=2.0, top=0.85, quiet_db=3.0):
    """第二版：相对门槛的重复次数 × 响度挑出最像副歌的块；再取与它相似、且不比它安静 3 dB 以上的块里最早的一处。"""
    bars = bars_from(beats, downs, len(x) / RATE)
    if len(bars) < 2 * length:
        return None, "小节太少"
    feats, loud = bar_features(x, beats, bars)
    rel = relative(feats @ feats.T)
    rep = repeated_blocks(rel, length, threshold=top)
    n = len(bars) - length + 1
    block_loud = np.array([loud[s : s + length].mean() for s in range(n)])
    z = (block_loud - block_loud.mean()) / (block_loud.std() + 1e-9)
    score = rep[:n] * np.clip(1 + 0.5 * z, 0.1, None)
    if score.max() <= 0:
        start = int(np.argmax(block_loud))
        why = "没有重复段，按响度"
    else:
        best = int(np.argmax(score))
        sim = np.array([np.mean([rel[best + i, s + i] for i in range(length)]) for s in range(n)])
        like = np.nonzero((sim >= top) & (block_loud >= block_loud[best] - quiet_db))[0]
        start = int(like[0]) if len(like) else best
        why = f"最像副歌的块在第 {best} 小节，重复 {int(rep[best])} 次；最早的一处在第 {start} 小节"
    nxt = start + length
    if nxt + length <= len(bars) and loud[nxt : nxt + length].mean() - loud[start : start + length].mean() >= rise_db:
        start = nxt
        why += "；下一段更响，前一段改判为铺垫"
    return bars[start][0], why


def novelty_bounds(ssm, w=4, min_gap=4):
    """Foote 的棋盘核在小节自相似矩阵上算新颖度，取局部极大值作段落边界。"""
    n = len(ssm)
    g = np.exp(-0.5 * (np.linspace(-1.5, 1.5, 2 * w) ** 2))
    sign = np.concatenate([-np.ones(w), np.ones(w)])
    kernel = np.outer(g, g) * -np.outer(sign, sign)
    padded = np.pad(ssm, w, mode="edge")
    nov = np.array([(kernel * padded[i : i + 2 * w, i : i + 2 * w]).sum() for i in range(n)])
    cut = nov.mean() + 0.5 * nov.std()
    bounds = [0]
    for i in range(1, n - 1):
        if nov[i] >= cut and nov[i] == nov[max(0, i - 2) : i + 3].max() and i - bounds[-1] >= min_gap:
            bounds.append(i)
    if n - bounds[-1] < min_gap and len(bounds) > 1:
        bounds.pop()
    return [*bounds, n]


def find_chorus_v3(x, beats, downs, rise_db=2.0, same=0.9):
    """第三版：按新颖度切段，相似的段归成一类；出现两次以上、平均最响的那一类是副歌，取它第一次出现。"""
    bars = bars_from(beats, downs, len(x) / RATE)
    if len(bars) < 16:
        return None, "小节太少"
    feats, loud = bar_features(x, beats, bars)
    bounds = novelty_bounds(feats @ feats.T)
    segs = list(itertools.pairwise(bounds))
    means = np.array([feats[a:b].mean(axis=0) for a, b in segs])
    means /= np.linalg.norm(means, axis=1, keepdims=True) + 1e-9
    labels = [-1] * len(segs)
    for i in range(len(segs)):
        if labels[i] < 0:
            labels[i] = i
            for j in range(i + 1, len(segs)):
                if labels[j] < 0 and means[i] @ means[j] >= same:
                    labels[j] = i
    seg_loud = np.array([loud[a:b].mean() for a, b in segs])
    groups = {lab: [k for k in range(len(segs)) if labels[k] == lab] for lab in set(labels)}
    repeated = {lab: ks for lab, ks in groups.items() if len(ks) >= 2}
    if not repeated:
        k = int(np.argmax(seg_loud))
        why = "没有重复的段，按响度"
    else:
        lab = max(repeated, key=lambda g: seg_loud[repeated[g]].mean())
        k = repeated[lab][0]
        why = f"{len(segs)} 段里，第 {k} 段所在的一类出现 {len(repeated[lab])} 次、平均最响"
    if k + 1 < len(segs) and seg_loud[k + 1] - seg_loud[k] >= rise_db:
        k += 1
        why += "；下一段更响，前一段改判为铺垫"
    return bars[segs[k][0]][0], why


def find_chorus(x, beats, downs, length=8, rise_db=2.0):
    """第一版，返回（副歌起点秒数，说明）。重复次数 × 响度挑出副歌所在的块，取它第一次出现；下一块更响 2 dB 以上时改判为铺垫。"""
    bars = bars_from(beats, downs, len(x) / RATE)
    if len(bars) < 2 * length:
        return None, "小节太少"
    feats, loud = bar_features(x, beats, bars)
    ssm = feats @ feats.T
    rep = repeated_blocks(ssm, length)
    block_loud = np.array([loud[s : s + length].mean() for s in range(len(bars) - length + 1)])
    z = (block_loud - block_loud.mean()) / (block_loud.std() + 1e-9)
    score = rep[: len(z)] * (1 + 0.5 * z)
    if score.max() <= 0:
        start = int(np.argmax(block_loud))
        why = "没有重复段，按响度"
    else:
        best = score.max()
        # 得分接近最高的起点里取最早的那个：副歌第一次出现。
        start = int(np.nonzero(score >= 0.9 * best)[0][0])
        why = f"重复 {int(rep[start])} 次"
    nxt = start + length
    if nxt + length <= len(bars) and loud[nxt : nxt + length].mean() - loud[start : start + length].mean() >= rise_db:
        start = nxt
        why += "；下一段更响，前一段改判为铺垫"
    return bars[start][0], why


# ---------------------------------------------------------------- 子命令


def cmd_export(args):
    import torch
    from beat_this.inference import load_model

    model = load_model(args.checkpoint, "cpu").eval()

    class Logits(torch.nn.Module):
        def __init__(self, inner):
            super().__init__()
            self.inner = inner

        def forward(self, spect):
            out = self.inner(spect)
            return out["beat"], out["downbeat"]

    dummy = torch.zeros(1, CHUNK, 128)
    torch.onnx.export(
        Logits(model),
        dummy,
        args.out,
        input_names=["spect"],
        output_names=["beat", "downbeat"],
        dynamic_axes={"spect": {1: "frames"}, "beat": {1: "frames"}, "downbeat": {1: "frames"}},
        opset_version=args.opset,
    )
    params = sum(p.numel() for p in model.parameters())
    print(json.dumps({"checkpoint": args.checkpoint, "params": params, "onnx_bytes": Path(args.out).stat().st_size}))


def cmd_check(args):
    import onnxruntime as ort
    import torch
    from beat_this.inference import Audio2Beats, load_model
    from beat_this.preprocessing import LogMelSpect

    x = decode(args.audio)
    ref_spect = LogMelSpect()(torch.tensor(x)).numpy()
    our_spect = log_mel(x)
    model = load_model(args.checkpoint, "cpu").eval()
    session = ort.InferenceSession(args.onnx, providers=["CPUExecutionProvider"])
    chunk = ref_spect[:CHUNK][None]
    with torch.inference_mode():
        out = model(torch.tensor(chunk))
    ort_beat, ort_down = session.run(None, {"spect": chunk.astype(np.float32)})
    ref_beats, ref_downs = Audio2Beats(args.checkpoint, dbn=False)(x, RATE)
    our_beats, our_downs = beats_onnx(session, x)
    print(
        json.dumps(
            {
                "spect_max_abs_diff": float(np.abs(ref_spect - our_spect).max()),
                "logit_max_abs_diff": float(
                    max(np.abs(out["beat"].numpy() - ort_beat).max(), np.abs(out["downbeat"].numpy() - ort_down).max())
                ),
                "beats": [len(ref_beats), len(our_beats)],
                "beats_equal": bool(len(ref_beats) == len(our_beats) and np.allclose(ref_beats, our_beats, atol=1e-6)),
                "downbeats": [len(ref_downs), len(our_downs)],
                "downbeats_equal": bool(
                    len(ref_downs) == len(our_downs) and np.allclose(ref_downs, our_downs, atol=1e-6)
                ),
            }
        )
    )


def cmd_songs(args):
    import mir_eval
    import onnxruntime as ort

    options = ort.SessionOptions()
    options.intra_op_num_threads = args.threads
    session = ort.InferenceSession(args.onnx, options, providers=["CPUExecutionProvider"])
    truth = json.loads(Path(args.truth).read_text(encoding="utf-8"))
    rows = []
    for song in truth:
        x = decode(song["audio"])
        t0 = time.perf_counter()
        beats, downs = beats_onnx(session, x)
        t1 = time.perf_counter()
        start, why = {"v1": find_chorus, "v2": find_chorus_v2, "v3": find_chorus_v3}[args.method](x, beats, downs)
        t2 = time.perf_counter()
        ref = json.loads(Path(song["allin1"]).read_text(encoding="utf-8"))
        bar = float(np.median(np.diff(downs))) if len(downs) > 1 else 0.0
        row = {
            "song": song["name"],
            "seconds": round(len(x) / RATE, 1),
            "beat_f": round(mir_eval.beat.f_measure(np.array(ref["beats"]), beats, 0.07), 3),
            "downbeat_f": round(mir_eval.beat.f_measure(np.array(ref["downbeats"]), downs, 0.07), 3),
            "beats_s": round(t1 - t0, 2),
            "chorus_s": round(t2 - t1, 2),
            "bar": round(bar, 2),
            "chorus": None if start is None else round(float(start), 1),
            "why": why,
        }
        if song.get("chorus") is not None:
            row["truth"] = song["chorus"]
            row["off_bars"] = None if start is None else round(abs(start - song["chorus"]) / bar, 1)
        rows.append(row)
        print(json.dumps(row, ensure_ascii=False), flush=True)
    peak = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss // 1024
    print(json.dumps({"threads": args.threads, "peak_rss_mib": peak}))


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("export")
    p.add_argument("--checkpoint", default="small0")
    p.add_argument("--opset", type=int, default=17)
    p.add_argument("--out", required=True)
    p = sub.add_parser("check")
    p.add_argument("--checkpoint", default="small0")
    p.add_argument("--onnx", required=True)
    p.add_argument("--audio", required=True)
    p = sub.add_parser("songs")
    p.add_argument("--onnx", required=True)
    p.add_argument("--truth", required=True, help="JSON：每首的 name、audio、allin1 与人工确认的副歌秒数 chorus")
    p.add_argument("--threads", type=int, default=0)
    p.add_argument("--method", choices=["v1", "v2", "v3"], default="v1")
    p.add_argument("--raw", action="store_true", help="小节特征不减全曲平均")
    args = parser.parse_args(argv)
    global CENTER
    CENTER = not getattr(args, "raw", False)
    {"export": cmd_export, "check": cmd_check, "songs": cmd_songs}[args.cmd](args)


if __name__ == "__main__":
    main(sys.argv[1:])
