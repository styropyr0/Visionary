import type { Meta } from "../lib/api";
import { TopBar } from "../components/Toolbar";

export function Alerts({ meta }: { meta?: Meta }) {
  void meta;
  return (
    <>
      <TopBar title="Alerts" section="Manage" />
      <div className="content">
        <div className="panel-state">Coming soon.</div>
      </div>
    </>
  );
}
