import { Ban, CheckCircle2, Clock, Loader, MinusCircle, XCircle } from "lucide-react";
import { OUTCOME_LABEL } from "../lib/ci";

const OUTCOME_ICON: Record<string, typeof CheckCircle2> = {
  success: CheckCircle2,
  passed: CheckCircle2,
  failure: XCircle,
  failed: XCircle,
  error: XCircle,
  timed_out: Clock,
  cancelled: Ban,
  skipped: MinusCircle,
  in_progress: Loader,
  queued: Clock,
};

export function Outcome({ outcome, compact }: { outcome: string; compact?: boolean }) {
  const Icon = OUTCOME_ICON[outcome] ?? MinusCircle;
  const label = OUTCOME_LABEL[outcome] ?? outcome;
  return (
    <span className={`ci-outcome ${outcome}`} title={compact ? label : undefined} aria-label={compact ? label : undefined} role={compact ? "img" : undefined}>
      <Icon size={13} aria-hidden />
      {!compact && label}
    </span>
  );
}

export function TestName({ test, file, service }: { test: string; file?: string; service?: string }) {
  return (
    <span className="ci-test-name" title={test}>
      <span className="mono">{test}</span>
      {(file || service) && <span className="dim">{[service, file].filter(Boolean).join(" · ")}</span>}
    </span>
  );
}
