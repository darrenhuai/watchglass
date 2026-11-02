# A stand-in for a rapidocr with no way to turn the text-direction check
# off: no use_cls keyword on the call and no params on the constructor. It
# counts the engines it builds, since each real one loads the models.
class _Out:
    def __init__(self, text):
        self.txts = (text,)
        self.scores = (0.99,)


class RapidOCR:
    built = 0

    def __init__(self, config_path=None):
        RapidOCR.built += 1

    def __call__(self, img_content):
        return _Out("call defaults, engines built %d" % RapidOCR.built)
