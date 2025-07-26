from rest_framework import serializers
from .models import Metric, MetricRecord, Panel, Dashboard


class MetricSerializer(serializers.ModelSerializer):
    class Meta:
        model = Metric
        fields = '__all__'
        read_only_fields = ['id', 'timestamp']


class MetricRecordSerializer(serializers.ModelSerializer):
    class Meta:
        model = MetricRecord
        fields = '__all__'
        read_only_fields = ['id', 'timestamp']


class PanelSerializer(serializers.ModelSerializer):
    metrics = MetricSerializer(many=True, read_only=True)
    metric_ids = serializers.PrimaryKeyRelatedField(
        many=True,
        write_only=True,
        queryset=Metric.objects.all(),
        source='metrics'
    )

    class Meta:
        model = Panel
        fields = ['id', 'title', 'panel_type', 'metrics', 'metric_ids']
        read_only_fields = ['id']

    def create(self, validated_data):
        metrics = validated_data.pop('metrics', [])
        panel = Panel.objects.create(**validated_data)
        panel.metrics.set(metrics)
        return panel

    def update(self, instance, validated_data):
        metrics = validated_data.pop('metrics', None)
        for attr, value in validated_data.items():
            setattr(instance, attr, value)
        instance.save()
        if metrics is not None:
            instance.metrics.set(metrics)
        return instance


class DashboardSerializer(serializers.ModelSerializer):
    panels = PanelSerializer(many=True, read_only=True)
    panel_ids = serializers.PrimaryKeyRelatedField(
        many=True,
        write_only=True,
        queryset=Panel.objects.all(),
        source='panels'
    )

    class Meta:
        model = Dashboard
        fields = ['id', 'title', 'description', 'panels', 'panel_ids']
        read_only_fields = ['id']

    def create(self, validated_data):
        panels = validated_data.pop('panels', [])
        dashboard = Dashboard.objects.create(**validated_data)
        dashboard.panels.set(panels)
        return dashboard

    def update(self, instance, validated_data):
        panels = validated_data.pop('panels', None)
        for attr, value in validated_data.items():
            setattr(instance, attr, value)
        instance.save()
        if panels is not None:
            instance.panels.set(panels)
        return instance
