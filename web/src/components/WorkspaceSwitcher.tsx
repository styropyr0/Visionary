import { Layers } from "lucide-react";
import { Select } from "./Select";
import { switchWorkspace, useSession } from "../lib/team";

/**
 * The workspace picker under the logo, for admins and for people in more
 * than one workspace. Picking one narrows every page to its services.
 */
export function WorkspaceSwitcher() {
  const { session } = useSession();
  const list = session?.workspaces ?? [];
  const admin = Boolean(session?.permissions?.admin);
  if (list.length === 0 || (list.length === 1 && !admin && !session?.workspace)) return null;
  const all = admin ? "All services" : "All my workspaces";
  const options = [{ value: "0", label: all }, ...list.map((w) => ({ value: String(w.id), label: w.name }))];
  return (
    <div className="workspace-switch">
      <Select
        value={String(session?.workspace ?? 0)}
        options={options}
        onChange={(v) => switchWorkspace(Number(v))}
        label="Workspace"
        icon={<Layers size={14} />}
        align="left"
      />
    </div>
  );
}
