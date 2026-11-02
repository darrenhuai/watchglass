# A stand-in for a rapidocr that fails on the picture itself. Its error
# mentions no use_cls, so the shim must report it rather than retry.
class RapidOCR:
    def __init__(self, config_path=None, params=None):
        pass

    def __call__(self, img_content, use_cls=None):
        raise TypeError("cannot decode image of type bytes")
