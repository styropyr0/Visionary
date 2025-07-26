from base import BaseCollector
from core.models import Metric
import xmlrpc.client as xenrpc


class XenCollector(BaseCollector):
    def collect(self) -> list:
        session = xenrpc.Session("http://{host}".format(**self.config))
        metrics = []
        for ref in session.xenapi.VM.get_all():
            name = session.xenapi.VM.get_name_label(ref)
            metrics.append(Metric(name="xen.vm.up", value=1, labels={"vm": name}))
        return metrics
