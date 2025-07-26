class VisionaryResponse:
    def __init__(self):
        pass

    def as_json_response(self):
        raise NotImplementedError("Collector must implement collect method")
