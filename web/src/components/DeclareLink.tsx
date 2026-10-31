import { Siren } from "lucide-react";
import { useSession } from "../lib/team";
import { declareHref } from "../lib/incidents";

export function DeclareLink({ kind, target, icon }: { kind: "alert" | "uptime" | "error" | "log"; target: string | number; icon?: boolean }) {
  const { can } = useSession();
  if (!can("edit")) return null;
  if (icon) {
    return (
      <a className="icon-btn" href={declareHref(kind, target)} title="Declare an incident" aria-label="Declare an incident">
        <Siren size={14} />
      </a>
    );
  }
  return (
    <a className="btn" href={declareHref(kind, target)}>
      <Siren size={14} />
      Declare incident
    </a>
  );
}
