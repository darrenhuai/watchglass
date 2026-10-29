# A stand-in for a rapidocr whose call takes no use_cls keyword and whose
# engine fails to load once the check is turned off in its config, for a
# reason that has nothing to do with the option. The shim must report that
# failure, not take it for a missing option and read with the check on.
class _Out:
    def __init__(self, text):
        self.txts = (text,)
        self.scores = (0.99,)


class RapidOCR:
    def __init__(self, config_path=None, params=None):
        if params:
            raise RuntimeError("onnxruntime: could not load model file rec.onnx")

    def __call__(self, img_content):
        return _Out("call defaults")
