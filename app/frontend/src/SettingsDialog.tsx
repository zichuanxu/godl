import { useEffect, useState } from "react";
import { Desktop, ProxyMode, Settings, SpeedRule, State, message } from "./api";
import { parseSpeed, speedText } from "./format";

type Props = { state: State; onClose: () => void; onSaved: () => void };

export default function SettingsDialog({ state, onClose, onSaved }: Props) {
  const [s, setS] = useState<Settings | null>(null);
  const [limit, setLimit] = useState("");
  const [rules, setRules] = useState<{ from: string; to: string; limit: string }[]>([]);
  const [sites, setSites] = useState("[]");
  const [autostart, setAutostart] = useState(state.autostart);
  const [error, setError] = useState("");

  useEffect(() => {
    Desktop.Settings()
      .then((loaded) => {
        setS(loaded);
        setLimit(speedText(loaded.speedLimit));
        setRules((loaded.schedule.speedRules ?? []).map((r) => ({ from: r.from, to: r.to, limit: speedText(r.limit) })));
        setSites(JSON.stringify(loaded.sites ?? [], null, 2));
      })
      .catch((err) => setError(message(err)));
  }, []);

  if (!s) {
    return (
      <div className="modal-backdrop" onClick={onClose}>
        <div className="modal">{error || "Loading…"}</div>
      </div>
    );
  }

  const set = (patch: Partial<Settings>) => setS(new Settings({ ...s, ...patch }));
  const windowed = !!s.schedule.start;

  async function save() {
    setError("");
    try {
      const next = new Settings({
        ...s!,
        speedLimit: parseSpeed(limit),
        schedule: { ...s!.schedule, speedRules: rules.map((r) => new SpeedRule({ from: r.from, to: r.to, limit: parseSpeed(r.limit) })) },
        sites: JSON.parse(sites),
      });
      await Desktop.SaveSettings(next);
      if (autostart !== state.autostart) await Desktop.SetAutostart(autostart);
      onSaved();
      onClose();
    } catch (err) {
      setError(message(err));
    }
  }

  const num = (v: string) => (v === "" ? 0 : parseInt(v, 10));
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal wide" onClick={(e) => e.stopPropagation()}>
        <h2>Settings</h2>
        <div className="grid">
          <fieldset>
            <legend>Downloads</legend>
            <label>
              Downloads at once
              <input type="number" min={1} max={64} value={s.maxConcurrent} onChange={(e) => set({ maxConcurrent: num(e.target.value) })} />
            </label>
            <label>
              Connections per download
              <input type="number" min={1} max={32} value={s.connections} onChange={(e) => set({ connections: num(e.target.value) })} />
            </label>
            <label>
              Connections per server
              <input type="number" min={1} max={64} value={s.hostConnections} onChange={(e) => set({ hostConnections: num(e.target.value) })} />
            </label>
            <label>
              Total speed limit
              <input placeholder="none, or 5M" value={limit} onChange={(e) => setLimit(e.target.value)} />
            </label>
          </fieldset>

          <fieldset>
            <legend>Schedule</legend>
            <label className="check">
              <input
                type="checkbox"
                checked={windowed}
                onChange={(e) => set({ schedule: { ...s.schedule, start: e.target.checked ? "01:00" : "", stop: e.target.checked ? "07:00" : "" } })}
              />
              Run the queue only between
            </label>
            <div className="row">
              <input type="time" disabled={!windowed} value={s.schedule.start ?? ""} onChange={(e) => set({ schedule: { ...s.schedule, start: e.target.value } })} />
              <span>and</span>
              <input type="time" disabled={!windowed} value={s.schedule.stop ?? ""} onChange={(e) => set({ schedule: { ...s.schedule, stop: e.target.value } })} />
            </div>
            <p className="note">Speed limits by time of day (the first match wins):</p>
            {rules.map((r, i) => (
              <div className="row" key={i}>
                <input type="time" value={r.from} onChange={(e) => setRules(rules.map((x, j) => (j === i ? { ...x, from: e.target.value } : x)))} />
                <input type="time" value={r.to} onChange={(e) => setRules(rules.map((x, j) => (j === i ? { ...x, to: e.target.value } : x)))} />
                <input placeholder="limit, e.g. 1M" value={r.limit} onChange={(e) => setRules(rules.map((x, j) => (j === i ? { ...x, limit: e.target.value } : x)))} />
                <button onClick={() => setRules(rules.filter((_, j) => j !== i))}>Remove</button>
              </div>
            ))}
            <button onClick={() => setRules([...rules, { from: "09:00", to: "18:00", limit: "1M" }])}>Add rule</button>
          </fieldset>

          <fieldset>
            <legend>Network</legend>
            <label>
              Proxy
              <select value={s.proxy.mode ?? ProxyMode.ModeSystem} onChange={(e) => set({ proxy: { ...s.proxy, mode: e.target.value as ProxyMode } })}>
                <option value={ProxyMode.ModeSystem}>System settings</option>
                <option value={ProxyMode.ModeNone}>No proxy</option>
                <option value={ProxyMode.ModeManual}>Manual</option>
              </select>
            </label>
            {s.proxy.mode === ProxyMode.ModeManual && (
              <label>
                Proxy URL
                <input placeholder="http://host:3128 or socks5://user:pass@host:1080" value={s.proxy.url ?? ""} onChange={(e) => set({ proxy: { ...s.proxy, url: e.target.value } })} />
              </label>
            )}
            <label>
              Site settings (JSON: host, connections, hostConnections, headers, username, password)
              <textarea rows={6} className="mono" value={sites} onChange={(e) => setSites(e.target.value)} />
            </label>
          </fieldset>

          <fieldset>
            <legend>Desktop</legend>
            <label className="check">
              <input type="checkbox" checked={s.desktop.clipboardMonitor} onChange={(e) => set({ desktop: { ...s.desktop, clipboardMonitor: e.target.checked } })} />
              Offer to download copied links and cURL commands
            </label>
            <label className="check">
              <input type="checkbox" checked={s.desktop.notifications} onChange={(e) => set({ desktop: { ...s.desktop, notifications: e.target.checked } })} />
              Notify when a download finishes or fails
            </label>
            <label className="check">
              <input type="checkbox" checked={s.desktop.askDirectory} onChange={(e) => set({ desktop: { ...s.desktop, askDirectory: e.target.checked } })} />
              Always ask where to save
            </label>
            <label className="check">
              <input type="checkbox" checked={s.desktop.openWhenDone} onChange={(e) => set({ desktop: { ...s.desktop, openWhenDone: e.target.checked } })} />
              Open files when they finish
            </label>
            <label className="check">
              <input type="checkbox" checked={autostart} onChange={(e) => setAutostart(e.target.checked)} />
              Start godl when I log in
            </label>
            <label>
              ffmpeg for “Convert to MP4” (empty: find it on PATH)
              <input placeholder="/opt/homebrew/bin/ffmpeg" value={s.desktop.ffmpeg ?? ""} onChange={(e) => set({ desktop: { ...s.desktop, ffmpeg: e.target.value } })} />
            </label>
            {(s.extraRoots ?? []).length > 0 && (
              <p className="note">Folders you picked: {(s.extraRoots ?? []).join(", ")}</p>
            )}
          </fieldset>
        </div>
        {error && <p className="error">{error}</p>}
        <div className="actions">
          <button onClick={onClose}>Cancel</button>
          <button className="primary" onClick={save}>Save</button>
        </div>
      </div>
    </div>
  );
}
