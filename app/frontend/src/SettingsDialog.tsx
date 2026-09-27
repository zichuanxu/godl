import { useState, useEffect, type ReactNode } from "react";
import { CircleAlert, Clock, Download, Globe, Monitor, Moon, Plus, Settings as SettingsIcon, SlidersHorizontal, Sun, Trash2, Wrench, type LucideIcon } from "lucide-react";
import { Desktop, ProxyMode, Settings, SpeedRule, State, message } from "./api";
import { parseSpeed, speedText } from "./format";
import { useI18n } from "./i18n";
import { Modal, Segmented, Switch } from "./ui";
import { applyTheme, loadTheme, saveTheme, type Theme } from "./theme";

type Props = { state: State; onClose: () => void; onSaved: () => void };
type Tab = "general" | "downloads" | "schedule" | "network" | "advanced";

const tabs: { id: Tab; icon: LucideIcon; label: "settings.general" | "settings.downloads" | "settings.schedule" | "settings.network" | "settings.advanced" }[] = [
  { id: "general", icon: SlidersHorizontal, label: "settings.general" },
  { id: "downloads", icon: Download, label: "settings.downloads" },
  { id: "schedule", icon: Clock, label: "settings.schedule" },
  { id: "network", icon: Globe, label: "settings.network" },
  { id: "advanced", icon: Wrench, label: "settings.advanced" },
];

/** Row is one setting: a label (and hint) on the left, its control on the right. */
function Row({ label, hint, children }: { label: ReactNode; hint?: ReactNode; children: ReactNode }) {
  return (
    <div className="set-row">
      <div className="set-text">
        <span className="set-label">{label}</span>
        {hint && <span className="hint">{hint}</span>}
      </div>
      <div className="set-control">{children}</div>
    </div>
  );
}

export default function SettingsDialog({ state, onClose, onSaved }: Props) {
  const { t, setLang } = useI18n();
  const [tab, setTab] = useState<Tab>("general");
  const [s, setS] = useState<Settings | null>(null);
  const [limit, setLimit] = useState("");
  const [rules, setRules] = useState<{ from: string; to: string; limit: string }[]>([]);
  const [sites, setSites] = useState("[]");
  const [autostart, setAutostart] = useState(state.autostart);
  const [theme, setTheme] = useState<Theme>(loadTheme);
  const [whenDone, setWhenDone] = useState("");
  const [initialWhenDone, setInitialWhenDone] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    Promise.all([Desktop.Settings(), Desktop.WhenDone()])
      .then(([loaded, action]) => {
        setS(loaded);
        setWhenDone(action);
        setInitialWhenDone(action);
        setLimit(speedText(loaded.speedLimit));
        setRules((loaded.schedule.speedRules ?? []).map((r) => ({ from: r.from, to: r.to, limit: speedText(r.limit) })));
        setSites(JSON.stringify(loaded.sites ?? [], null, 2));
      })
      .catch((err) => setError(message(err)));
  }, []);

  async function save() {
    if (!s) return;
    setError("");
    try {
      const next = new Settings({
        ...s,
        speedLimit: parseSpeed(limit),
        schedule: { ...s.schedule, speedRules: rules.map((r) => new SpeedRule({ from: r.from, to: r.to, limit: parseSpeed(r.limit) })) },
        sites: JSON.parse(sites),
      });
      const saved = await Desktop.SaveSettings(next);
      if (whenDone !== initialWhenDone) await Desktop.SetWhenDone(whenDone);
      if (autostart !== state.autostart) await Desktop.SetAutostart(autostart);
      saveTheme(theme);
      applyTheme(theme);
      setLang(saved.desktop.language ?? "");
      onSaved();
      onClose();
    } catch (err) {
      setError(message(err));
    }
  }

  const footer = (
    <>
      {error && (
        <p className="form-error grow">
          <CircleAlert size={14} />
          <span className="ellipsis" title={error}>
            {error}
          </span>
        </p>
      )}
      <button onClick={onClose}>{t("common.cancel")}</button>
      <button className="primary" disabled={!s} onClick={save}>
        {t("common.save")}
      </button>
    </>
  );

  if (!s) {
    return (
      <Modal icon={SettingsIcon} title={t("settings.title")} size="wide" onClose={onClose} footer={footer}>
        <p className="muted">{error ? "" : t("common.loading")}</p>
      </Modal>
    );
  }

  const set = (patch: Partial<Settings>) => setS(new Settings({ ...s, ...patch }));
  const desk = (patch: Partial<Settings["desktop"]>) => set({ desktop: { ...s.desktop, ...patch } });
  const num = (v: string) => (v === "" ? 0 : parseInt(v, 10));
  const windowed = !!s.schedule.start;
  const rule = (i: number, patch: Partial<(typeof rules)[number]>) => setRules(rules.map((x, j) => (j === i ? { ...x, ...patch } : x)));

  return (
    <Modal icon={SettingsIcon} title={t("settings.title")} size="wide" onClose={onClose} footer={footer}>
      <div className="settings">
        <nav className="settings-nav">
          {tabs.map((x) => (
            <button key={x.id} className={`nav-item ${tab === x.id ? "on" : ""}`} onClick={() => setTab(x.id)}>
              <x.icon size={15} />
              {t(x.label)}
            </button>
          ))}
          <div className="grow" />
          <span className="settings-version">{t("settings.version", { version: state.version })}</span>
        </nav>

        <div className="settings-pane">
          {tab === "general" && (
            <>
              <Row label={t("settings.language")}>
                <select value={s.desktop.language ?? ""} onChange={(e) => desk({ language: e.target.value })}>
                  <option value="">{t("settings.languageSystem")}</option>
                  <option value="en">English</option>
                  <option value="zh-CN">简体中文</option>
                </select>
              </Row>
              <Row label={t("settings.theme")}>
                <Segmented
                  value={theme}
                  onChange={setTheme}
                  options={[
                    { value: "system", label: t("settings.themeSystem"), icon: Monitor },
                    { value: "light", label: t("settings.themeLight"), icon: Sun },
                    { value: "dark", label: t("settings.themeDark"), icon: Moon },
                  ]}
                />
              </Row>
              <div className="set-group">
                <Switch checked={autostart} onChange={setAutostart} label={t("settings.autostart")} />
                <Switch checked={s.desktop.notifications} onChange={(v) => desk({ notifications: v })} label={t("settings.notifications")} />
                <Switch checked={s.desktop.clipboardMonitor} onChange={(v) => desk({ clipboardMonitor: v })} label={t("settings.clipboard")} />
                <Switch checked={s.desktop.checkUpdates} onChange={(v) => desk({ checkUpdates: v })} label={t("settings.checkUpdates")} />
              </div>
            </>
          )}

          {tab === "downloads" && (
            <>
              <Row label={t("settings.maxConcurrent")}>
                <input type="number" min={1} max={64} value={s.maxConcurrent} onChange={(e) => set({ maxConcurrent: num(e.target.value) })} />
              </Row>
              <Row label={t("settings.connections")}>
                <input type="number" min={1} max={32} value={s.connections} onChange={(e) => set({ connections: num(e.target.value) })} />
              </Row>
              <Row label={t("settings.hostConnections")}>
                <input type="number" min={1} max={64} value={s.hostConnections} onChange={(e) => set({ hostConnections: num(e.target.value) })} />
              </Row>
              <Row label={t("settings.speedLimit")}>
                <input placeholder={t("settings.speedHint")} value={limit} onChange={(e) => setLimit(e.target.value)} />
              </Row>
              <div className="set-group">
                <Switch checked={s.desktop.askDirectory} onChange={(v) => desk({ askDirectory: v })} label={t("settings.askDirectory")} />
                <Switch checked={s.desktop.openWhenDone} onChange={(v) => desk({ openWhenDone: v })} label={t("settings.openWhenDone")} />
              </div>
              <Row label={t("settings.whenDone")} hint={t("settings.whenDoneHint")}>
                <select aria-label={t("settings.whenDone")} value={whenDone} onChange={(e) => setWhenDone(e.target.value)}>
                  <option value="">{t("whenDone.nothing")}</option>
                  <option value="sleep">{t("whenDone.sleep")}</option>
                  <option value="shutdown">{t("whenDone.shutdown")}</option>
                </select>
              </Row>
            </>
          )}

          {tab === "schedule" && (
            <>
              <div className="set-group">
                <Switch
                  checked={windowed}
                  onChange={(v) => set({ schedule: { ...s.schedule, start: v ? "01:00" : "", stop: v ? "07:00" : "" } })}
                  label={t("settings.window")}
                  hint={t("settings.windowHint")}
                />
                {windowed && (
                  <div className="time-range">
                    <input type="time" value={s.schedule.start ?? ""} onChange={(e) => set({ schedule: { ...s.schedule, start: e.target.value } })} />
                    <span className="muted">{t("settings.and")}</span>
                    <input type="time" value={s.schedule.stop ?? ""} onChange={(e) => set({ schedule: { ...s.schedule, stop: e.target.value } })} />
                  </div>
                )}
              </div>
              <div className="section-head">
                <div>
                  <span className="set-label">{t("settings.rules")}</span>
                  <span className="hint">{t("settings.rulesHint")}</span>
                </div>
                <button className="small" onClick={() => setRules([...rules, { from: "09:00", to: "18:00", limit: "1M" }])}>
                  <Plus size={14} />
                  {t("settings.addRule")}
                </button>
              </div>
              {rules.length === 0 ? (
                <p className="muted empty-rules">{t("settings.rulesEmpty")}</p>
              ) : (
                <div className="rules">
                  {rules.map((r, i) => (
                    <div className="rule" key={i}>
                      <input type="time" value={r.from} onChange={(e) => rule(i, { from: e.target.value })} />
                      <span className="muted">–</span>
                      <input type="time" value={r.to} onChange={(e) => rule(i, { to: e.target.value })} />
                      <input className="grow" placeholder={t("settings.ruleLimit")} value={r.limit} onChange={(e) => rule(i, { limit: e.target.value })} />
                      <button className="icon ghost" title={t("common.remove")} aria-label={t("common.remove")} onClick={() => setRules(rules.filter((_, j) => j !== i))}>
                        <Trash2 size={15} />
                      </button>
                    </div>
                  ))}
                </div>
              )}
            </>
          )}

          {tab === "network" && (
            <>
              <Row label={t("settings.proxy")}>
                <select value={s.proxy.mode ?? ProxyMode.ModeSystem} onChange={(e) => set({ proxy: { ...s.proxy, mode: e.target.value as ProxyMode } })}>
                  <option value={ProxyMode.ModeSystem}>{t("settings.proxySystem")}</option>
                  <option value={ProxyMode.ModeNone}>{t("settings.proxyNone")}</option>
                  <option value={ProxyMode.ModeManual}>{t("settings.proxyManual")}</option>
                </select>
              </Row>
              {s.proxy.mode === ProxyMode.ModeManual && (
                <label className="field">
                  <span className="field-label">{t("settings.proxyURL")}</span>
                  <input className="mono" placeholder="http://host:3128 · socks5://user:pass@host:1080" value={s.proxy.url ?? ""} onChange={(e) => set({ proxy: { ...s.proxy, url: e.target.value } })} spellCheck={false} />
                </label>
              )}
              <label className="field">
                <span className="field-label">{t("settings.sites")}</span>
                <textarea rows={9} className="mono" value={sites} onChange={(e) => setSites(e.target.value)} spellCheck={false} />
                <span className="hint">{t("settings.sitesHint")}</span>
              </label>
            </>
          )}

          {tab === "advanced" && (
            <>
              <label className="field">
                <span className="field-label">{t("settings.ffmpeg")}</span>
                <input className="mono" placeholder="/opt/homebrew/bin/ffmpeg" value={s.desktop.ffmpeg ?? ""} onChange={(e) => desk({ ffmpeg: e.target.value })} spellCheck={false} />
                <span className="hint">{t("settings.ffmpegHint")}</span>
              </label>
              {(s.extraRoots ?? []).length > 0 && (
                <div className="field">
                  <span className="field-label">{t("settings.extraRoots")}</span>
                  <ul className="paths">
                    {(s.extraRoots ?? []).map((p) => (
                      <li key={p} className="mono">
                        {p}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </Modal>
  );
}
