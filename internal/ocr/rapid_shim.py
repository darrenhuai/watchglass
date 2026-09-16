import json, logging, sys
logging.disable(logging.WARNING)  # rapidocr logs model paths (INFO) and "detection result is empty" (WARNING) to stderr
try:
    from rapidocr import RapidOCR
except ImportError as e:
    sys.stderr.write("rapidocr: %s (pip install rapidocr onnxruntime)\n" % e)
    sys.exit(3)
try:
    res = RapidOCR()(sys.stdin.buffer.read())
except Exception as e:
    sys.stderr.write("rapidocr: %s: %s\n" % (type(e).__name__, e))
    sys.exit(1)
lines = []
if res.txts:
    for t, s in zip(res.txts, res.scores):
        lines.append({"text": t, "score": float(s)})
json.dump({"lines": lines}, sys.stdout)
