import { useState, type FormEvent } from "react";
import { post } from "../lib/api";

export function Login({ onDone }: { onDone: () => void }) {
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    post("/api/v1/login", { password })
      .then(onDone)
      .catch((err: Error) => setError(err.message === "wrong password" ? "That password is not correct." : err.message))
      .finally(() => setBusy(false));
  };

  return (
    <div className="login">
      <form className="card login-card" onSubmit={submit}>
        <h1 className="page-title">NetFlow Plus</h1>
        <p className="page-sub">Sign in to view network traffic.</p>
        <label className="field">
          <span>Password</span>
          <input className="input" type="password" autoFocus autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </label>
        {error && <div className="error-note" role="alert">{error}</div>}
        <button className="btn primary" type="submit" disabled={busy || password === ""}>{busy ? "Signing in…" : "Sign in"}</button>
      </form>
    </div>
  );
}
