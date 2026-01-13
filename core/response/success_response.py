from .visionary_response import VisionaryResponse
from django.http import JsonResponse


class SuccessResponse(VisionaryResponse):
    def __init__(self, message: str = "Successful", code: int = 200, data: dict = None):
        self.message = message
        self.code = code
        self.data = data or {}

    def as_json_response(self):
        response_data = {
            "message": self.message,
            "status": "success",
            "code": self.code,
        }
        if self.data:
            response_data["data"] = self.data
        return JsonResponse(data=response_data, status=self.code)
