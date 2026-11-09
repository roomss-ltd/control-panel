"""The control panel's dictation worker (stt.go): faster-whisper kept warm in one process.

Reads one JSON line per clip ({"path": ..., "lang": ..., "hotwords": ...}) on stdin and answers one JSON line
({"text": ...} or {"error": ...}) on stdout. It prints {"ready": true} once the model is loaded.
"""
import json, os, subprocess, sys
import numpy as np
from faster_whisper import WhisperModel

FFMPEG = os.environ.get("STT_FFMPEG", "ffmpeg")


def pcm(path):
    """Decode any clip the browser records to 16 kHz mono float, as whisper wants it (ffmpeg, not PyAV)."""
    raw = subprocess.run([FFMPEG, "-nostdin", "-v", "error", "-i", path, "-f", "s16le", "-ac", "1", "-ar", "16000", "-"],
                         capture_output=True, check=True, timeout=120).stdout
    return np.frombuffer(raw, np.int16).astype(np.float32) / 32768.0


model = WhisperModel(os.environ.get("STT_MODEL", "small"), device="cpu", compute_type="int8",
                     cpu_threads=int(os.environ.get("STT_THREADS", "4")))
print(json.dumps({"ready": True}), flush=True)
for line in sys.stdin:
    try:
        req = json.loads(line)
        # beam 3 over greedy: fewer errors on mumbled speech for ~1 s; temperature 0 alone: no re-decoding a hard clip
        # at higher temperatures (the slow outliers), and no worse on the test clips
        segs, _ = model.transcribe(pcm(req["path"]), language=req.get("lang") or None, beam_size=3, temperature=0,
                                   vad_filter=True, hotwords=req.get("hotwords") or None,
                                   condition_on_previous_text=False)
        out = {"text": " ".join(s.text.strip() for s in segs).strip()}
    except Exception as e:  # a bad clip answers with its error; the worker lives on
        out = {"error": str(e)[:300]}
    print(json.dumps(out), flush=True)
