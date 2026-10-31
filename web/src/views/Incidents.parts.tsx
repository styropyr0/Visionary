import { CheckCircle2, Crosshair, Eye, Search } from "lucide-react";
import { SEVERITIES, incidentHref, sevLabel, statusLabel, type IncidentStatus, type Severity } from "../lib/incidents";

export function openIncident(id: number | null) {
  window.location.hash = id ? incidentHref(id) : "#/incidents";
}

const STATUS_ICON: Record<IncidentStatus, typeof Search> = { investigating: Search, identified: Crosshair, monitoring: Eye, resolved: CheckCircle2 };

export function StatusBadge({ status }: { status: IncidentStatus }) {
  const Icon = STATUS_ICON[status] ?? Search;
  return (
    <span className={`inc-status ${status}`}>
      <Icon size={13} aria-hidden />
      {statusLabel(status)}
    </span>
  );
}

export function SevTag({ severity }: { severity: Severity }) {
  return (
    <span className={`inc-sev ${severity}`} title={SEVERITIES.find((s) => s.key === severity)?.hint}>
      {sevLabel(severity)}
    </span>
  );
}
