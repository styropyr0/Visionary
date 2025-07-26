from visionary_response import VisionaryResponse
from exceptions.visionary_base_exception import VisionaryBaseException
from django.http import JsonResponse


class ErrorResponse(VisionaryResponse):
    def __init__(self, exception: VisionaryBaseException):
        self.exception = exception

    def as_json_response(self):
        return JsonResponse(data=dict(self.exception), status=self.exception.code)
