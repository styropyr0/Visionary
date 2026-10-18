import { useEffect, useRef, useState, type ClipboardEvent, type FormEvent, type KeyboardEvent } from "react";
import {
  AlertTriangle,
  ArrowRight,
  AtSign,
  Bell,
  Eye,
  EyeOff,
  LineChart,
  Loader2,
  Lock,
  MapPin,
  MessageSquareText,
  Phone,
  Server,
  ShieldCheck,
  User,
} from "lucide-react";
import { api, ApiError, type AuthConfig, type SessionInfo } from "../lib/api";
import { BrandMark } from "../components/Sidebar";

interface Props {
  /** Why the previous session ended, if it wasn't a normal sign-out. */
  notice?: string;
  onSignedIn: (s: SessionInfo) => void;
}

/**
 * Sign-in is delegated to the configured API, so this screen renders whatever
 * flow the server's auth config describes: identifier + one-time code, or
 * identifier + password.
 */
export function Login({ notice, onSignedIn }: Props) {
  const [cfg, setCfg] = useState<AuthConfig | null>(null);
  const [cfgError, setCfgError] = useState("");

  useEffect(() => {
    api<AuthConfig>("/api/auth/config")
      .then(setCfg)
      .catch((e: unknown) => setCfgError(e instanceof Error ? e.message : String(e)));
  }, []);

  return (
    <main className="auth">
      <BrandPanel provider={cfg?.provider} delegated={cfg?.type === "otp" || cfg?.type === "password"} />
      <section className="auth-main">
        {cfg ? (
          <SignInForm cfg={cfg} notice={notice} onSignedIn={onSignedIn} />
        ) : cfgError ? (
          <div className="auth-form">
            <div className="auth-alert error" role="alert">
              <AlertTriangle size={15} />
              <span>Sign-in isn't available: {cfgError}</span>
            </div>
          </div>
        ) : (
          <Loader2 size={20} className="spin" color="var(--text-3)" aria-label="Loading" />
        )}
      </section>
    </main>
  );
}

function SignInForm({ cfg, notice, onSignedIn }: { cfg: AuthConfig; notice?: string; onSignedIn: (s: SessionInfo) => void }) {
  const otp = cfg.type === "otp";
  const shared = cfg.type === "shared";
  const codeLength = cfg.code_length ?? 6;
  const [step, setStep] = useState<"identifier" | "code">("identifier");
  const [identifier, setIdentifier] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [digits, setDigits] = useState<string[]>(() => Array(codeLength).fill(""));
  const [sentTo, setSentTo] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [resendIn, setResendIn] = useState(0);
  const boxes = useRef<(HTMLInputElement | null)[]>([]);
  const wantsLocation = cfg.location === "optional" || cfg.location === "required";
  const position = useRef<{ latitude: number; longitude: number } | null>(null);
  const [locationDenied, setLocationDenied] = useState(false);

  const pendingLocation = useRef<Promise<{ latitude: number; longitude: number } | null> | null>(null);

  /**
   * Asks the browser once and shares the answer between callers. A permission
   * prompt nobody answers resolves to null after 15 seconds.
   */
  const locate = (): Promise<{ latitude: number; longitude: number } | null> => {
    if (!wantsLocation) return Promise.resolve(null);
    if (position.current) return Promise.resolve(position.current);
    if (!navigator.geolocation) {
      setLocationDenied(true);
      return Promise.resolve(null);
    }
    if (pendingLocation.current) return pendingLocation.current;
    const ask = new Promise<{ latitude: number; longitude: number } | null>((resolve) => {
      window.setTimeout(() => resolve(position.current), 15_000);
      navigator.geolocation.getCurrentPosition(
        (p) => {
          position.current = { latitude: p.coords.latitude, longitude: p.coords.longitude };
          setLocationDenied(false);
          resolve(position.current);
        },
        () => {
          setLocationDenied(true);
          resolve(null);
        },
        { timeout: 10_000, maximumAge: 600_000 },
      );
    }).finally(() => {
      pendingLocation.current = null;
    });
    pendingLocation.current = ask;
    return ask;
  };

  useEffect(() => {
    if (resendIn <= 0) return;
    const id = window.setTimeout(() => setResendIn((s) => s - 1), 1000);
    return () => window.clearTimeout(id);
  }, [resendIn]);

  const fail = (e: unknown) => setError(e instanceof Error ? e.message : String(e));

  const sendCode = async () => {
    setPending(true);
    setError("");
    try {
      // Ask for location now, so the browser prompt doesn't interrupt the
      // code step; sign-in waits for the answer, sending the code doesn't.
      void locate();
      const res = await api<{ sent_to: string }>("/api/auth/start", { method: "POST", json: { identifier } });
      setSentTo(res.sent_to || identifier);
      setDigits(Array(codeLength).fill(""));
      setStep("code");
      setResendIn(cfg.resend_seconds ?? 30);
      window.setTimeout(() => boxes.current[0]?.focus(), 0);
    } catch (e) {
      fail(e);
    } finally {
      setPending(false);
    }
  };

  const complete = async (secret: { code?: string; password?: string }) => {
    setPending(true);
    setError("");
    try {
      const pos = await locate();
      if (!pos && cfg.location === "required") {
        throw new Error(`${cfg.provider} needs your location to sign in. Allow location access in your browser and try again.`);
      }
      const s = await api<SessionInfo>("/api/auth/complete", { method: "POST", json: { identifier, ...secret, ...(pos ?? {}) } });
      onSignedIn(s);
    } catch (e) {
      fail(e);
      setPending(false);
      if (otp) {
        if (e instanceof ApiError && e.status === 403) {
          // Not allowed in: the code is spent, so go back to the start.
          setStep("identifier");
        } else {
          setDigits(Array(codeLength).fill(""));
          window.setTimeout(() => boxes.current[0]?.focus(), 0);
        }
      }
    }
  };

  const setDigit = (i: number, v: string) => {
    const next = [...digits];
    next[i] = v;
    setDigits(next);
    if (v && i < codeLength - 1) boxes.current[i + 1]?.focus();
    if (next.every(Boolean)) complete({ code: next.join("") });
  };

  const onBoxKey = (i: number, e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Backspace" && !digits[i] && i > 0) {
      boxes.current[i - 1]?.focus();
      const next = [...digits];
      next[i - 1] = "";
      setDigits(next);
    } else if (e.key === "ArrowLeft" && i > 0) {
      boxes.current[i - 1]?.focus();
    } else if (e.key === "ArrowRight" && i < codeLength - 1) {
      boxes.current[i + 1]?.focus();
    }
  };

  const onPaste = (e: ClipboardEvent<HTMLInputElement>) => {
    const pasted = e.clipboardData.getData("text").replace(/\D/g, "").slice(0, codeLength);
    if (!pasted) return;
    e.preventDefault();
    const next = Array.from({ length: codeLength }, (_, i) => pasted[i] ?? "");
    setDigits(next);
    boxes.current[Math.min(pasted.length, codeLength - 1)]?.focus();
    if (pasted.length === codeLength) complete({ code: pasted });
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (pending) return;
    if (!otp) complete({ password });
    else if (step === "identifier") sendCode();
    else if (digits.every(Boolean)) complete({ code: digits.join("") });
  };

  const IdIcon = cfg.identifier.input === "tel" ? Phone : cfg.identifier.input === "email" ? AtSign : User;
  const roles = (cfg.allowed_roles ?? []).map((r) => r.toLowerCase()).join(" or ");

  return (
    <form className="auth-form" onSubmit={submit} noValidate>
      <div className="auth-form-head">
        <BrandMark />
        <h1>{step === "code" ? "Check your messages" : "Sign in to Visionary"}</h1>
        <p>
          {step === "code"
            ? `Enter the ${codeLength}-digit code we sent you.`
            : shared
              ? "Enter the team password to continue."
              : `Use your ${cfg.provider} account. Access is limited to ${roles} accounts.`}
        </p>
      </div>

      {notice && !error && (
        <div className="auth-alert info">
          <ShieldCheck size={15} />
          <span>{notice}</span>
        </div>
      )}

      {step === "identifier" ? (
        <div className="fade-in" style={{ display: "flex", flexDirection: "column", gap: 14 }} key="id">
          {!shared && (
            <label className="field">
              <span>{cfg.identifier.label}</span>
              <div className="input-wrap">
                <IdIcon size={16} />
                <input
                  className="input"
                  type={cfg.identifier.input ?? "text"}
                  inputMode={cfg.identifier.normalize === "digits" ? "numeric" : undefined}
                  autoComplete={cfg.identifier.input === "tel" ? "tel-national" : cfg.identifier.input === "email" ? "email" : "username"}
                  placeholder={cfg.identifier.placeholder}
                  value={identifier}
                  onChange={(e) =>
                    setIdentifier(cfg.identifier.normalize === "digits" ? e.target.value.replace(/\D/g, "").slice(0, 15) : e.target.value)
                  }
                  autoFocus
                  required
                />
              </div>
            </label>
          )}
          {!otp && (
            <label className="field">
              <span>{shared ? "Team password" : "Password"}</span>
              <div className="input-wrap">
                <Lock size={16} />
                <input
                  className="input"
                  type={showPassword ? "text" : "password"}
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  required
                />
                <button
                  type="button"
                  className="icon-btn reveal"
                  onClick={() => setShowPassword(!showPassword)}
                  aria-label={showPassword ? "Hide password" : "Show password"}
                >
                  {showPassword ? <EyeOff size={15} /> : <Eye size={15} />}
                </button>
              </div>
            </label>
          )}
        </div>
      ) : (
        <div className="fade-in" style={{ display: "flex", flexDirection: "column", gap: 14 }} key="code">
          <div className="sent-to">
            <span className="who">
              <IdIcon size={14} />
              Sent to <strong>{sentTo}</strong>
            </span>
            <button
              type="button"
              className="link-btn"
              onClick={() => {
                setStep("identifier");
                setError("");
              }}
            >
              Change
            </button>
          </div>
          <div className={`code-boxes ${error ? "error" : ""}`} role="group" aria-label={`${codeLength}-digit code`}>
            {digits.map((d, i) => (
              <input
                key={i}
                ref={(el) => {
                  boxes.current[i] = el;
                }}
                value={d}
                inputMode="numeric"
                autoComplete={i === 0 ? "one-time-code" : "off"}
                aria-label={`Digit ${i + 1}`}
                maxLength={1}
                disabled={pending}
                onChange={(e) => setDigit(i, e.target.value.replace(/\D/g, "").slice(-1))}
                onKeyDown={(e) => onBoxKey(i, e)}
                onPaste={onPaste}
                onFocus={(e) => e.target.select()}
              />
            ))}
          </div>
          <div style={{ display: "flex", justifyContent: "space-between", fontSize: 13, color: "var(--text-3)" }}>
            <span>Didn't get it?</span>
            <button type="button" className="link-btn" disabled={resendIn > 0 || pending} onClick={sendCode}>
              {resendIn > 0 ? `Resend in ${resendIn}s` : "Resend code"}
            </button>
          </div>
        </div>
      )}

      {error && (
        <div className="auth-alert error" role="alert">
          <AlertTriangle size={15} />
          <span>{error}</span>
        </div>
      )}

      {wantsLocation && (
        <div className="auth-alert info">
          <MapPin size={15} />
          <span>
            {locationDenied
              ? cfg.location === "required"
                ? "Allow location access to sign in."
                : "Signing in without location."
              : `Shares your location with ${cfg.provider}.`}
          </span>
        </div>
      )}

      <button
        className="btn primary"
        type="submit"
        disabled={
          pending ||
          (!shared && !identifier) ||
          (!otp && !password) ||
          (otp && step === "code" && !digits.every(Boolean))
        }
      >
        {pending && <Loader2 size={15} className="spin" />}
        {!otp
          ? pending
            ? "Signing in…"
            : "Sign in"
          : step === "identifier"
            ? pending
              ? "Sending code…"
              : "Continue"
            : pending
              ? "Verifying…"
              : "Verify and sign in"}
        {!pending && <ArrowRight size={15} />}
      </button>

      <div className="auth-meta">
        <ShieldCheck size={13} />
        {shared ? "Shared team access" : `Secured by your ${cfg.provider} sign-in`}
      </div>
    </form>
  );
}

function BrandPanel({ provider, delegated }: { provider?: string; delegated: boolean }) {
  return (
    <aside className="auth-brand" aria-hidden="false">
      <div className="auth-logo">
        <BrandMark />
        Visionary
      </div>
      <div className="auth-pitch">
        <h2>
          Monitoring for {delegated && provider ? provider : "your API"}, <span>on your own database.</span>
        </h2>
        <p>Requests, errors, server health and app performance, with alerts when something changes.</p>
        <ul className="auth-features">
          <li>
            <span className="ico">
              <LineChart size={15} />
            </span>
            <span>
              <strong>Requests and errors</strong>
              Every API call with its route, status, latency and user. Errors are grouped by where they happen.
            </span>
          </li>
          <li>
            <span className="ico">
              <Server size={15} />
            </span>
            <span>
              <strong>Servers and apps</strong>
              CPU, memory, disk and database pool from each server, and screen loads and crashes from the web and mobile apps.
            </span>
          </li>
          <li>
            <span className="ico">
              <Bell size={15} />
            </span>
            <span>
              <strong>Alerts and uptime</strong>
              Rules on any metric and scheduled checks on your endpoints, sent to Slack, email or a webhook.
            </span>
          </li>
          <li>
            <span className="ico">
              <MessageSquareText size={15} />
            </span>
            <span>
              <strong>Questions in plain words</strong>
              Ask about the data and get a chart back, with the query it ran.
            </span>
          </li>
        </ul>
      </div>
      <BrandArt />
    </aside>
  );
}

/** Decorative chart composition: a latency line with a spike over traffic bars. */
function BrandArt() {
  const bars = [34, 42, 38, 51, 47, 58, 62, 55, 49, 70, 96, 88, 61, 54, 57, 52, 60, 64, 59, 66];
  const line = [22, 24, 23, 27, 25, 29, 31, 28, 26, 38, 74, 63, 33, 29, 30, 28, 31, 33, 30, 34];
  const w = 520;
  const h = 150;
  const step = w / bars.length;
  const y = (v: number) => h - 8 - (v / 100) * (h - 24);
  const path = line.map((v, i) => `${i ? "L" : "M"}${(i + 0.5) * step},${y(v)}`).join("");
  const peak = 10;
  return (
    <svg className="auth-art" viewBox={`0 0 ${w} ${h + 26}`} role="img" aria-label="Illustration of a latency spike over traffic">
      {[0.25, 0.5, 0.75].map((f) => (
        <line key={f} x1="0" x2={w} y1={h * f} y2={h * f} stroke="rgba(255,255,255,0.07)" />
      ))}
      {bars.map((v, i) => (
        <rect
          key={i}
          x={i * step + step * 0.22}
          width={step * 0.56}
          y={y(v)}
          height={h - 8 - y(v)}
          rx="3"
          fill={i === peak || i === peak + 1 ? "rgba(217,89,38,0.55)" : "rgba(57,135,229,0.28)"}
        />
      ))}
      <path d={path} fill="none" stroke="#86b6ef" strokeWidth="2" strokeLinejoin="round" strokeLinecap="round" />
      <circle cx={(peak + 0.5) * step} cy={y(line[peak])} r="5" fill="#0f1012" stroke="#f0efec" strokeWidth="2" />
      <g transform={`translate(${(peak + 0.5) * step + 12}, ${y(line[peak]) - 8})`}>
        <rect width="214" height="30" rx="6" fill="rgba(15,16,18,0.92)" stroke="rgba(255,255,255,0.14)" />
        <text x="107" y="19" fill="#f0efec" fontSize="11.5" fontFamily="inherit" textAnchor="middle">
          p95 4.2s · /api/search · 5xx ×6
        </text>
      </g>
      <text x="0" y={h + 20} fill="#898781" fontSize="11" fontFamily="inherit">
        12:00
      </text>
      <text x={w} y={h + 20} fill="#898781" fontSize="11" fontFamily="inherit" textAnchor="end">
        18:00
      </text>
    </svg>
  );
}
