import os


class VisionarySettings:
    def __init__(self):
        self.refresh_interval = int(os.getenv("VISIONARY_REFRESH_INTERVAL", 15))
        self.enable_debug = bool(int(os.getenv("VISIONARY_DEBUG", 0)))
        self.auth_classes = []
        self.permission_classes = []


settings = VisionarySettings()
