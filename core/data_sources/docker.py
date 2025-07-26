from base import BaseCollector
from core.models import Metric
import docker


class DockerCollector(BaseCollector):
    def collect(self) -> list:
        client = docker.from_env()
        return (
            [
                Metric(
                    name="docker.container.running",
                    value=1,
                    labels={"container": c.name},
                )
                for c in client.containers.list()
            ]
            + [
                Metric(
                    name="docker.container.stopped",
                    value=1,
                    labels={"container": c.name},
                )
                for c in client.containers.list(all=True, filters={"status": "exited"})
            ]
            + [
                Metric(
                    name="docker.container.paused",
                    value=1,
                    labels={"container": c.name},
                )
                for c in client.containers.list(all=True, filters={"status": "paused"})
            ]
            + [
                Metric(
                    name="docker.container.restarting",
                    value=1,
                    labels={"container": c.name},
                )
                for c in client.containers.list(
                    all=True, filters={"status": "restarting"}
                )
            ]
            + [
                Metric(
                    name="docker.container.dead", value=1, labels={"container": c.name}
                )
                for c in client.containers.list(all=True, filters={"status": "dead"})
            ]
            + [
                Metric(
                    name="docker.container.total",
                    value=len(client.containers.list(all=True)),
                )
            ]
            + [Metric(name="docker.image.total", value=len(client.images.list()))]
            + [Metric(name="docker.network.total", value=len(client.networks.list()))]
            + [Metric(name="docker.volume.total", value=len(client.volumes.list()))]
            + [Metric(name="docker.swarm.nodes", value=len(client.nodes.list()))]
            + [Metric(name="docker.swarm.services", value=len(client.services.list()))]
            + [Metric(name="docker.swarm.tasks", value=len(client.tasks.list()))]
            + [
                Metric(
                    name="docker.swarm.leader",
                    value=1 if client.swarm.attrs.get("Leader") else 0,
                )
            ]
            + [
                Metric(
                    name="docker.swarm.state",
                    value=1 if client.swarm.attrs.get("State") == "active" else 0,
                )
            ]
            + [Metric(name="docker.swarm.cluster_size", value=len(client.nodes.list()))]
            + [Metric(name="docker.swarm.version", value=client.version()["Version"])]
            + [
                Metric(
                    name="docker.swarm.cluster_id",
                    value=client.swarm.attrs.get("Cluster", {}).get("ID", ""),
                )
            ]
            + [
                Metric(
                    name="docker.swarm.join_tokens",
                    value=client.swarm.attrs.get("JoinTokens", {}),
                )
            ]
            + [
                Metric(
                    name="docker.swarm.orchestration",
                    value=client.swarm.attrs.get("Orchestration", {}),
                )
            ]
            + [
                Metric(
                    name="docker.swarm.dispatcher",
                    value=client.swarm.attrs.get("Dispatcher", {}),
                )
            ]
            + [
                Metric(
                    name="docker.swarm.encryption_config",
                    value=client.swarm.attrs.get("EncryptionConfig", {}),
                )
            ]
            + [
                Metric(
                    name="docker.swarm.task_history_retention_limit",
                    value=client.swarm.attrs.get("TaskHistoryRetentionLimit", 0),
                )
            ]
            + [
                Metric(
                    name="docker.swarm.log_driver",
                    value=client.swarm.attrs.get("LogDriver", {}),
                )
            ]
        )
