from core.base import BaseCollector
from core.models import Metric
from kubernetes import client, config


class KubernetesCollector(BaseCollector):
    """Kubernetes cluster metrics collector"""

    def collect(self) -> list:
        metrics = []
        try:
            config.load_kube_config()
            v1 = client.CoreV1Api()
            apps_v1 = client.AppsV1Api()

            # Node metrics
            nodes = v1.list_node().items
            for node in nodes:
                status = 1 if node.status.conditions[-1].status == "True" else 0
                metrics.append(
                    Metric(name="k8s.node.up", value=status, labels={"node": node.metadata.name})
                )

                # Node capacity
                if node.status.allocatable:
                    cpu = node.status.allocatable.get("cpu", "0").rstrip("m")
                    memory = node.status.allocatable.get("memory", "0").rstrip("Ki")
                    metrics.append(
                        Metric(name="k8s.node.cpu_allocatable", value=float(cpu) if cpu else 0, labels={"node": node.metadata.name})
                    )
                    metrics.append(
                        Metric(name="k8s.node.memory_allocatable_kb", value=float(memory) if memory else 0, labels={"node": node.metadata.name})
                    )

            # Pod metrics
            pods = v1.list_pod_for_all_namespaces().items
            metrics.append(Metric(name="k8s.pod.count", value=len(pods)))

            pod_by_status = {}
            for pod in pods:
                status = pod.status.phase
                pod_by_status[status] = pod_by_status.get(status, 0) + 1

            for status, count in pod_by_status.items():
                metrics.append(
                    Metric(name="k8s.pod.count_by_status", value=count, labels={"status": status})
                )

            # Deployment metrics
            deployments = apps_v1.list_deployment_for_all_namespaces().items
            metrics.append(Metric(name="k8s.deployment.count", value=len(deployments)))

            for deployment in deployments:
                replicas = deployment.spec.replicas or 0
                ready_replicas = deployment.status.ready_replicas or 0
                metrics.append(
                    Metric(
                        name="k8s.deployment.replicas",
                        value=replicas,
                        labels={"name": deployment.metadata.name, "namespace": deployment.metadata.namespace}
                    )
                )
                metrics.append(
                    Metric(
                        name="k8s.deployment.ready_replicas",
                        value=ready_replicas,
                        labels={"name": deployment.metadata.name, "namespace": deployment.metadata.namespace}
                    )
                )

            # StatefulSet metrics
            statefulsets = apps_v1.list_stateful_set_for_all_namespaces().items
            metrics.append(Metric(name="k8s.statefulset.count", value=len(statefulsets)))

            # DaemonSet metrics
            daemonsets = apps_v1.list_daemon_set_for_all_namespaces().items
            metrics.append(Metric(name="k8s.daemonset.count", value=len(daemonsets)))

            # Namespace metrics
            namespaces = v1.list_namespace().items
            metrics.append(Metric(name="k8s.namespace.count", value=len(namespaces)))

        except Exception as e:
            metrics.append(Metric(name="k8s.error", value=1, labels={"message": str(e)}))

        return metrics
