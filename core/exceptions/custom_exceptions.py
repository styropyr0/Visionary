from visionary_base_exception import VisionaryBaseException


class InvalidMetricException(VisionaryBaseException):
    def __init__(self):
        super().__init__(
            "Invalid metric provided",
            "Please check the metric configuration you have provided. It seems, it is not a valid one.",
            400,
        )


class DashboardNotFoundException(VisionaryBaseException):
    def __init__(self):
        super().__init__(
            "Not found",
            "The request dashboard was not found. Did you mispell it?",
            404,
        )
