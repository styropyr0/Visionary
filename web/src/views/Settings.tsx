import type { SessionUser } from "../lib/api";
import { TopBar } from "../components/Toolbar";

export function Settings({ user }: { user: SessionUser | null }) {
  void user;
  return (
    <>
      <TopBar title="Settings" section="Manage" />
      <div className="content">
        <div className="panel-state">Coming soon.</div>
      </div>
    </>
  );
}
