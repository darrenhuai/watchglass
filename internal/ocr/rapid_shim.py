import json, logging, sys
logging.disable(logging.WARNING)  # rapidocr logs model paths (INFO) and "detection result is empty" (WARNING) to stderr
# watchglass passes --no-cls. rapidocr's text-direction check turns seven-segment
# digits upside down and then reads them with full confidence (12:05 as 50:21).
# With it off, text that really is upside down in the picture (a camera mounted
# the wrong way up) isn't turned here any more: the watch's rotate: 180 turns
# the crop before it reaches this shim.
NO_CLS = "--no-cls" in sys.argv[1:]
try:
    from rapidocr import RapidOCR
except ImportError as e:
    sys.stderr.write("rapidocr: %s (pip install rapidocr onnxruntime)\n" % e)
    sys.exit(3)


def rejects(e, *names):
    # An older rapidocr turns down an option it doesn't know with a TypeError
    # (unexpected keyword), a ValueError (3.x's "Unknown parameter: use_cls")
    # or a KeyError on a config key, and names the option. Anything else is a
    # real failure and is reported, not retried.
    return isinstance(e, (TypeError, ValueError, KeyError)) and any(n in str(e) for n in names)


def read(img):
    # One engine for the keyword call and, if it comes to that, the read with
    # the check on: each RapidOCR() loads the models, which takes seconds.
    engine = RapidOCR()
    if not NO_CLS:
        return engine(img), "default"
    try:
        return engine(img, use_cls=False), "off"
    except Exception as e:
        if not rejects(e, "use_cls"):
            raise
    # rapidocr 2.0 to 5.0 take use_cls on the call. Before that, the same
    # setting is in the engine's config, which needs an engine of its own.
    try:
        off = RapidOCR(params={"Global.use_cls": False})
    except Exception as e:
        if not rejects(e, "params", "use_cls"):
            raise
    else:
        return off(img), "off"
    sys.stderr.write("rapidocr: this rapidocr can't turn its text-direction check off; reading with it on\n")
    return engine(img), "default"


try:
    res, cls = read(sys.stdin.buffer.read())
except Exception as e:
    sys.stderr.write("rapidocr: %s: %s\n" % (type(e).__name__, e))
    sys.exit(1)
lines = []
if res.txts:
    for t, s in zip(res.txts, res.scores):
        lines.append({"text": t, "score": float(s)})
json.dump({"lines": lines, "cls": cls}, sys.stdout)
