from django.db import models


class Metric(models.Model):
    SOURCE_CHOICES = [
        ("cpu", "CPU"),
        ("gpu", "GPU"),
        ("memory", "Memory"),
        ("disk", "Disk"),
        ("network", "Network"),
        ("custom", "Custom"),
    ]

    UNIT_CHOICES = [
        ("%", "Percentage"),
        ("ms", "Milliseconds"),
        ("s", "Seconds"),
        ("B", "Bytes"),
        ("KB", "Kilobytes"),
        ("MB", "Megabytes"),
        ("GB", "Gigabytes"),
        ("count", "Count"),
        ("°C", "Celsius"),
        ("Hz", "Hertz"),
        ("ops", "Operations"),
        ("", "None"),
    ]

    id = models.AutoField(primary_key=True)
    name = models.CharField(max_length=255)
    label = models.JSONField(default=dict)
    source_type = models.CharField(max_length=50, choices=SOURCE_CHOICES)
    unit = models.CharField(max_length=20, choices=UNIT_CHOICES, default="")
    created_at = models.DateTimeField(auto_now_add=True)

    class Meta:
        db_table = "visionary_metrics"
        ordering = ["-id"]

    def __str__(self):
        return f"{self.name} ({self.source_type})"


class MetricRecord(models.Model):
    id = models.AutoField(primary_key=True)
    metric = models.ForeignKey(Metric, on_delete=models.CASCADE, related_name="records")
    value = models.FloatField()
    timestamp = models.DateTimeField(auto_now_add=True)

    class Meta:
        db_table = "visionary_metric_records"
        ordering = ["-timestamp"]

    def __str__(self):
        return f"{self.metric.name} = {self.value}{self.metric.unit}"


class Panel(models.Model):
    PANEL_TYPE_CHOICES = [
        ("docker", "Docker"),
        ("elastic_search", "Elastic Search"),
        ("hyper_v", "Hyper-V"),
        ("influx_db", "Influx DB"),
        ("kubernetes", "Kubernetes"),
        ("kvm", "KVM"),
        ("mysql", "MySQL"),
        ("openstack", "OpenStack"),
        ("postgres", "Postgres"),
        ("prometheus", "Prometheus"),
        ("proxmox", "Proxmox"),
        ("redis", "Redis"),
        ("system", "System"),
        ("vmware", "VMware"),
        ("xen", "Xen"),
    ]

    id = models.AutoField(primary_key=True)
    title = models.CharField(max_length=125)
    metrics = models.ManyToManyField(Metric, related_name="panels")
    panel_type = models.CharField(max_length=50, choices=PANEL_TYPE_CHOICES)

    class Meta:
        db_table = "visionary_panels"
        ordering = ["-id"]

    def __str__(self):
        return self.title


class Dashboard(models.Model):
    id = models.AutoField(primary_key=True)
    title = models.CharField(max_length=100)
    description = models.TextField(null=True, blank=True)
    panels = models.ManyToManyField(Panel, related_name="dashboards")

    class Meta:
        db_table = "visionary_dashboards"
        ordering = ["-id"]

    def __str__(self):
        return self.title
