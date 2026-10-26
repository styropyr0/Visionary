import { useState } from "react";
import { Copy, ShieldCheck } from "lucide-react";
import { api } from "../lib/api";
import { useQuery } from "../lib/useQuery";
import { Card } from "../views/Settings.profiling";
import { useToast } from "./Toast";

interface TwoStepState {
  available: boolean;
  required: boolean;
  enabled: boolean;
  recovery_left?: number;
}

interface SetupInfo {
  secret: string;
  uri: string;
  qr?: string;
}

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/**
 * Setting up an authenticator app: scan the code, confirm one code, keep the
 * recovery codes. onDone runs once the recovery codes are dismissed.
 */
export function TwoStepSetup({ onDone, onCancel }: { onDone: () => void; onCancel?: () => void }) {
  const toast = useToast();
  const [setup, setSetup] = useState<SetupInfo | null>(null);
  const [code, setCode] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const start = async () => {
    setBusy(true);
    setError("");
    try {
      setSetup(await api<SetupInfo>("/api/me/two-step/setup", { method: "POST", json: {} }));
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const confirm = async () => {
    setBusy(true);
    setError("");
    try {
      const res = await api<{ recovery_codes: string[] }>("/api/me/two-step/enable", { method: "POST", json: { code } });
      setCodes(res.recovery_codes);
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      toast("ok", "Copied.");
    } catch {
      toast("err", "Couldn't copy; select the text instead.");
    }
  };

  if (codes) {
    return (
      <div className="two-step">
        <p>
          Two-step sign-in is on. Keep these recovery codes somewhere safe: each one signs you in once if you lose your phone. They won't be shown again.
        </p>
        <ol className="two-step-codes mono">
          {codes.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ol>
        <div className="acct-form-actions">
          <button type="button" className="btn sm" onClick={() => copy(codes.join("\n"))}>
            <Copy size={14} /> Copy codes
          </button>
          <button type="button" className="btn primary sm" onClick={onDone}>
            I've saved them
          </button>
        </div>
      </div>
    );
  }
  if (!setup) {
    return (
      <div className="two-step">
        <p>Use an authenticator app such as Google Authenticator, Microsoft Authenticator, 1Password or Authy. After your password, Visionary asks for the 6-digit code it shows.</p>
        {error && <div className="conf-json-err">{error}</div>}
        <div className="acct-form-actions">
          {onCancel && (
            <button type="button" className="btn sm" onClick={onCancel}>
              Cancel
            </button>
          )}
          <button type="button" className="btn primary sm" disabled={busy} onClick={start}>
            {busy ? "Starting…" : "Set up an authenticator app"}
          </button>
        </div>
      </div>
    );
  }
  return (
    <form
      className="two-step"
      onSubmit={(e) => {
        e.preventDefault();
        void confirm();
      }}
    >
      <div className="two-step-scan">
        {setup.qr && <img src={setup.qr} alt="QR code for your authenticator app" width={180} height={180} />}
        <div>
          <p>Scan the code with your authenticator app, or type this key into it:</p>
          <button type="button" className="two-step-key mono" title="Copy" onClick={() => copy(setup.secret)}>
            {setup.secret.replace(/(.{4})/g, "$1 ").trim()}
            <Copy size={13} />
          </button>
        </div>
      </div>
      <label className="field">
        <span>The 6-digit code it shows</span>
        <input
          className="input mono"
          inputMode="numeric"
          autoComplete="one-time-code"
          autoFocus
          maxLength={7}
          value={code}
          onChange={(e) => setCode(e.target.value.replace(/[^\d ]/g, ""))}
          style={{ maxWidth: 160 }}
        />
      </label>
      {error && <div className="conf-json-err">{error}</div>}
      <div className="acct-form-actions">
        {onCancel && (
          <button type="button" className="btn sm" onClick={onCancel}>
            Cancel
          </button>
        )}
        <button type="submit" className="btn primary sm" disabled={busy || code.replace(/\s/g, "").length !== 6}>
          {busy ? "Checking…" : "Turn on"}
        </button>
      </div>
    </form>
  );
}

/** Settings → Members & roles: your own two-step sign-in. */
export function TwoStepCard() {
  const toast = useToast();
  const [run, setRun] = useState(0);
  const q = useQuery(`two-step#${run}`, (signal) => api<TwoStepState>("/api/me/two-step", { signal }));
  const [setting, setSetting] = useState(false);
  const [disabling, setDisabling] = useState(false);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const d = q.data;
  if (!d?.available) return null;
  const disable = async () => {
    setBusy(true);
    try {
      await api("/api/me/two-step/disable", { method: "POST", json: { password } });
      toast("ok", "Two-step sign-in is off.");
      setDisabling(false);
      setPassword("");
      setRun((n) => n + 1);
    } catch (e) {
      toast("err", message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card
      title="Two-step sign-in"
      description={
        d.enabled
          ? `On. Signing in asks for a code from your authenticator app. ${d.recovery_left ?? 0} recovery code(s) left.`
          : d.required
            ? "Required on this Visionary: set it up to keep using it."
            : "Off. Turn it on so a stolen password alone can't sign in as you."
      }
      actions={
        d.enabled ? (
          <span className="pill two-step-on">
            <ShieldCheck size={13} /> On
          </span>
        ) : undefined
      }
    >
      <div className="settings-body pset">
        {d.enabled ? (
          disabling ? (
            <form
              className="two-step"
              onSubmit={(e) => {
                e.preventDefault();
                void disable();
              }}
            >
              <label className="field">
                <span>Your password, to turn it off</span>
                <input className="input conf-input" type="password" autoComplete="current-password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} />
              </label>
              <div className="acct-form-actions">
                <button type="button" className="btn sm" onClick={() => setDisabling(false)}>
                  Cancel
                </button>
                <button type="submit" className="btn sm" disabled={busy || !password}>
                  {busy ? "Turning off…" : "Turn off"}
                </button>
              </div>
            </form>
          ) : (
            <div className="acct-form-actions">
              <button type="button" className="btn sm" onClick={() => setDisabling(true)}>
                Turn off
              </button>
            </div>
          )
        ) : setting ? (
          <TwoStepSetup
            onCancel={() => setSetting(false)}
            onDone={() => {
              setSetting(false);
              setRun((n) => n + 1);
            }}
          />
        ) : (
          <div className="acct-form-actions">
            <button type="button" className="btn primary sm" onClick={() => setSetting(true)}>
              Set up
            </button>
          </div>
        )}
      </div>
    </Card>
  );
}

/** The whole screen while an account that must use two-step sign-in has none yet. */
export function TwoStepRequired({ onDone, onSignOut }: { onDone: () => void; onSignOut: () => void }) {
  return (
    <div className="content two-step-page">
      <section className="panel">
        <header className="settings-card-head">
          <div>
            <h3 className="panel-title">Set up two-step sign-in</h3>
            <p className="settings-desc">Your admin requires it on this Visionary. It takes a minute with an authenticator app on your phone.</p>
          </div>
        </header>
        <div className="settings-body">
          <TwoStepSetup onDone={onDone} />
        </div>
        <div className="settings-body">
          <button type="button" className="link-btn" onClick={onSignOut}>
            Sign out instead
          </button>
        </div>
      </section>
    </div>
  );
}
