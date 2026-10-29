# A stand-in for a rapidocr whose call takes no use_cls keyword (Python
# raises TypeError: unexpected keyword argument) but whose config still has
# Global.use_cls.
class _Out:
    def __init__(self, text):
        self.txts = (text,)
        self.scores = (0.99,)


class RapidOCR:
    def __init__(self, config_path=None, params=None):
        self.params = params or {}

    def __call__(self, img_content):
        return _Out("call config use_cls=%s" % self.params.get("Global.use_cls"))
