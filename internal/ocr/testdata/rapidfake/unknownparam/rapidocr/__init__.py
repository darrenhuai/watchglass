# A stand-in for a rapidocr that checks the call's keywords itself, the way
# 3.x's update_params does, and rejects use_cls with a ValueError.
class _Out:
    def __init__(self, text):
        self.txts = (text,)
        self.scores = (0.99,)


class RapidOCR:
    def __init__(self, config_path=None, params=None):
        self.params = params or {}

    def __call__(self, img_content, **kwargs):
        for key in kwargs:
            raise ValueError(f"Unknown parameter: {key}")
        return _Out("call config use_cls=%s" % self.params.get("Global.use_cls"))
