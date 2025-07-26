class VisionaryBaseException(Exception):
    def __init__(self, title: str, message: str, code: int):
        self.title = title
        self.message = message
        self.code = code

    def __iter__(self):
        yield from {
            "title": self.title,
            "message": self.message,
            "code": self.code,
        }.items()

    def throw(self):
        raise self
