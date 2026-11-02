# A stand-in for the rapidocr package as pip installs it today (2.0 to 5.0):
# use_cls is a keyword of the call and a key of the config. It reads nothing;
# its one line of text says how the shim called it.
class _Out:
    def __init__(self, text):
        self.txts = (text,)
        self.scores = (0.99,)


class RapidOCR:
    def __init__(self, config_path=None, params=None):
        self.params = params or {}

    def __call__(self, img_content, use_det=None, use_cls=None, use_rec=None):
        return _Out("call use_cls=%s config use_cls=%s" % (use_cls, self.params.get("Global.use_cls")))
