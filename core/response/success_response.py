from django.http import JsonResponse


class SuccessResponse:
    def __init__(self, message: str = "Successful", code: int = 200):
        self.message = message
        self.code = code

    def as_json_response(self):
        return JsonResponse(data=self.message, status=self.code)
