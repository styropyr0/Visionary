from rest_framework.views import APIView
from rest_framework.response import Response
from core.settings import settings
from .models import Dashboard
from .serializers import DashboardSerializer
from core.response.error_response import ErrorResponse
from core.response.success_response import SuccessResponse
from core.exceptions.custom_exceptions import *


class DashboardCreateView(APIView):
    authentication_classes = settings.auth_classes
    permission_classes = settings.permission_classes

    def post(self, request):
        serializer = DashboardSerializer(data=request.data)
        if serializer.is_valid():
            serializer.save()
            return SuccessResponse(
                code=201, message="Dashboard created"
            ).as_json_response()
        return ErrorResponse(InvalidMetricException()).as_json_response()


class DashboardDetailView(APIView):
    authentication_classes = settings.auth_classes
    permission_classes = settings.permission_classes

    def get_object(self, id):
        try:
            return Dashboard.objects.get(id=id)
        except Dashboard.DoesNotExist:
            raise DashboardNotFoundException()

    def get(self, request, id):
        try:
            dashboard = self.get_object(id)
        except DashboardNotFoundException:
            return ErrorResponse(DashboardNotFoundException()).as_json_response()
        serializer = DashboardSerializer(dashboard)
        return Response(serializer.data)

    def put(self, request, id):
        dashboard = self.get_object(id)
        serializer = DashboardSerializer(dashboard, data=request.data)
        if serializer.is_valid():
            serializer.save()
            return SuccessResponse(
                code=200, message="Dashboard updated"
            ).as_json_response()
        return ErrorResponse(InvalidMetricException()).as_json_response()

    def delete(self, request, id):
        try:
            dashboard = self.get_object(id)
        except DashboardNotFoundException:
            return ErrorResponse(DashboardNotFoundException()).as_json_response()
        dashboard.delete()
        return SuccessResponse(code=204, message="Dashboard deleted").as_json_response()
