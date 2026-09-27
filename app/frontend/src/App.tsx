import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Browser, Events } from "@wailsio/runtime";
import {
  ArrowDownToLine,
  CircleAlert,
  CircleCheck,
  Clipboard,
  Ellipsis,
  FileArchive,
  FileImage,
  FileMusic,
  FilePlay,
  FileText,
  File as FileIcon,
  Film,
  FolderInput,
  FolderOpen,
  FolderOutput,
  Gauge,
  Hourglass,
  Inbox,
  KeyRound,
  Layers,
  Moon,
  Package,
  Pause,
  Play,
  Plus,
  Power,
  RotateCw,
  Search,
  Settings as SettingsIcon,
  Sparkles,
  SquareArrowOutUpRight,
  Trash2,
  X,
  type LucideIcon,
} from "lucide-react";
import { Desktop, EventType, Item, State, Status, message } from "./api";
import AddDialog, { type AddPrefill } from "./AddDialog";
import SettingsDialog from "./SettingsDialog";
import { CountdownDialog, DeleteDialog, ReauthDialog } from "./dialogs";
import Sparkline from "./Sparkline";
import { baseName, bytes, eta, fileKind, looksLikeCurl, looksLikeURL, speed, type FileKind } from "./format";
import { I18n, resolveLang, translator, useI18n, type T } from "./i18n";
import { Menu, type MenuEntry } from "./ui";

type Sample = { at: number; done: number; bps: number };
type Filter = "all" | "active" | "paused" | "completed" | "failed";
type Toast = { kind: "ok" | "error"; text: string };
type Action = Exclude<MenuEntry, "separator">;

const kindIcon: Record<FileKind, LucideIcon> = {
  video: FilePlay,
  audio: FileMusic,
  archive: FileArchive,
  image: FileImage,
  document: FileText,
  program: Package,
  file: FileIcon,
};

const filters: { id: Filter; icon: LucideIcon; label: "nav.all" | "nav.active" | "nav.paused" | "nav.completed" | "nav.failed" }[] = [
  { id: "all", icon: Layers, label: "nav.all" },
  { id: "active", icon: ArrowDownToLine, label: "nav.active" },
  { id: "paused", icon: Pause, label: "nav.paused" },
  { id: "completed", icon: CircleCheck, label: "nav.completed" },
  { id: "failed", icon: CircleAlert, label: "nav.failed" },
];

function matches(item: Item, f: Filter): boolean {
  switch (f) {
    case "active":
      return item.status === Status.StatusRunning || item.status === Status.StatusQueued;
    case "paused":
      return item.status === Status.StatusPaused;
    case "completed":
      return item.status === Status.StatusCompleted;
    case "failed":
      return item.status === Status.StatusFailed;
  }
  return true;
}

const isMac = navigator.userAgent.includes("Mac");
const needsAuth = (item?: Item) => !!item && item.status === Status.StatusFailed && /HTTP (401|403)\b/.test(item.error ?? "");
const isTyping = (e: KeyboardEvent) => e.target instanceof HTMLElement && /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName);

/** App owns the interface language and renders the window. */
export default function App() {
  const [langSetting, setLangSetting] = useState<string>();
  const lang = resolveLang(langSetting);
  const i18n = useMemo(() => ({ lang, t: translator(lang), setLang: setLangSetting }), [lang]);
  useEffect(() => {
    Desktop.Settings()
      .then((s) => setLangSetting(s.desktop.language ?? ""))
      .catch(() => setLangSetting(""));
  }, []);
  useEffect(() => {
    document.documentElement.lang = lang;
    Desktop.SetLocale(lang).catch(() => {});
  }, [lang]);
  return (
    <I18n.Provider value={i18n}>
      <Shell />
    </I18n.Provider>
  );
}

function Shell() {
  const { t } = useI18n();
  const [state, setState] = useState<State | null>(null);
  const [items, setItems] = useState<Map<string, Item>>(new Map());
  const [selected, setSelected] = useState<string | null>(null);
  const [filter, setFilter] = useState<Filter>("all");
  const [query, setQuery] = useState("");
  const [adding, setAdding] = useState<AddPrefill | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [deleting, setDeleting] = useState<Item | null>(null);
  const [reauth, setReauth] = useState<Item | null>(null);
  const [clip, setClip] = useState<string | null>(null);
  const [toast, setToast] = useState<Toast | null>(null);
  const [history, setHistory] = useState<number[]>(() => Array(60).fill(0));
  const [whenDone, setWhenDone] = useState("");
  const [canConvert, setCanConvert] = useState(false);
  const [converting, setConverting] = useState<string | null>(null);
  const [countdown, setCountdown] = useState<{ action: string; left: number } | null>(null);
  const [dragging, setDragging] = useState(false);
  const [context, setContext] = useState<{ x: number; y: number; id: string } | null>(null);
  const [dismissed, setDismissed] = useState(() => {
    try {
      return localStorage.getItem("dismissedUpdate") ?? "";
    } catch {
      return "";
    }
  });
  const samples = useRef(new Map<string, Sample>());
  const search = useRef<HTMLInputElement>(null);
  const dragDepth = useRef(0);
  const [, setTick] = useState(0);

  const fail = useCallback((err: unknown) => setToast({ kind: "error", text: message(err) }), []);
  const ok = useCallback((text: string) => setToast({ kind: "ok", text }), []);

  const reload = useCallback(async () => {
    try {
      const list = await Desktop.List();
      setItems(new Map((list ?? []).map((i) => [i.id, i])));
    } catch (err) {
      fail(err);
    }
  }, [fail]);

  useEffect(() => {
    Desktop.State().then(setState).catch(fail);
    Desktop.WhenDone().then(setWhenDone).catch(() => {});
    Desktop.CanConvert().then(setCanConvert).catch(() => {});
    reload();
    const offDownload = Events.On("download", (ev) => {
      const e = ev.data;
      const item = e.item;
      if (e.type === EventType.EventDeleted) {
        samples.current.delete(item.id);
        setItems((prev) => {
          const next = new Map(prev);
          next.delete(item.id);
          return next;
        });
        return;
      }
      const now = performance.now();
      const last = samples.current.get(item.id);
      if (item.status === Status.StatusRunning) {
        if (last && now > last.at && item.completed >= last.done) {
          const instant = ((item.completed - last.done) * 1000) / (now - last.at);
          samples.current.set(item.id, { at: now, done: item.completed, bps: last.bps ? last.bps * 0.7 + instant * 0.3 : instant });
        } else {
          samples.current.set(item.id, { at: now, done: item.completed, bps: 0 });
        }
      } else {
        samples.current.delete(item.id);
      }
      setItems((prev) => new Map(prev).set(item.id, item));
    });
    const offResync = Events.On("resync", () => reload());
    const offStopped = Events.On("stopped", () => Desktop.State().then(setState).catch(fail));
    const offClip = Events.On("clipboard", (ev) => setClip(ev.data));
    const offDone = Events.On("queue-done", (ev) => setCountdown({ action: ev.data, left: 30 }));
    const offUpdate = Events.On("update", () => Desktop.State().then(setState).catch(() => {}));
    const timer = window.setInterval(() => {
      const now = performance.now();
      let total = 0;
      for (const [id, s] of samples.current) {
        if (now - s.at > 3000) samples.current.set(id, { ...s, bps: 0 });
        total += s.bps;
      }
      setHistory((h) => [...h.slice(-59), total]);
      setTick((n) => n + 1);
    }, 1000);
    return () => {
      offDownload();
      offResync();
      offStopped();
      offClip();
      offDone();
      offUpdate();
      window.clearInterval(timer);
    };
  }, [reload, fail]);

  // The completion action runs when the countdown reaches zero unless cancelled.
  useEffect(() => {
    if (!countdown) return;
    if (countdown.left <= 0) {
      setCountdown(null);
      setWhenDone("");
      Desktop.PerformWhenDone(countdown.action).catch(fail);
      return;
    }
    const timer = window.setTimeout(() => setCountdown({ ...countdown, left: countdown.left - 1 }), 1000);
    return () => window.clearTimeout(timer);
  }, [countdown, fail]);

  useEffect(() => {
    if (!toast) return;
    const timer = window.setTimeout(() => setToast(null), toast.kind === "error" ? 8000 : 3500);
    return () => window.clearTimeout(timer);
  }, [toast]);

  const chooseWhenDone = (action: string) => {
    setWhenDone(action);
    Desktop.SetWhenDone(action).catch((err) => {
      setWhenDone("");
      fail(err);
    });
  };

  const importQueue = async () => {
    try {
      const res = await Desktop.ImportQueue();
      if (res.failed?.length) setToast({ kind: "error", text: t("toast.imported", { n: res.added, failed: res.failed.join("\n") }) });
    } catch (err) {
      fail(err);
    }
  };

  const exportQueue = async () => {
    try {
      if (await Desktop.ExportQueue()) ok(t("toast.exported"));
    } catch (err) {
      fail(err);
    }
  };

  const run = (fn: () => Promise<unknown>) => () => {
    fn().catch(fail);
  };

  const convert = (item: Item) => {
    setConverting(item.id);
    Desktop.ConvertToMP4(item.id)
      .then((out) => ok(t("toast.saved", { name: baseName(out) })))
      .catch(fail)
      .finally(() => setConverting(null));
  };

  // RFC 3339 strings with trimmed fractions do not sort as text.
  const all = useMemo(() => [...items.values()].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt)), [items]);
  const counts = useMemo(() => Object.fromEntries(filters.map((f) => [f.id, all.filter((i) => matches(i, f.id)).length])) as Record<Filter, number>, [all]);
  const q = query.trim().toLowerCase();
  const rows = all.filter((i) => matches(i, filter) && (!q || baseName(i.destination).toLowerCase().includes(q) || i.url.toLowerCase().includes(q)));
  const current = selected ? items.get(selected) : undefined;
  const totalSpeed = history[history.length - 1] ?? 0;
  const modalOpen = !!(adding || settingsOpen || deleting || reauth || countdown);

  const actions = (item: Item): Action[] => {
    const list: Action[] = [];
    if (item.status === Status.StatusRunning || item.status === Status.StatusQueued) list.push({ icon: Pause, label: t("row.pause"), onSelect: run(() => Desktop.Pause(item.id)) });
    if (item.status === Status.StatusPaused) list.push({ icon: Play, label: t("row.resume"), onSelect: run(() => Desktop.Resume(item.id)) });
    if (item.status === Status.StatusFailed) list.push({ icon: RotateCw, label: t("row.retry"), onSelect: run(() => Desktop.Retry(item.id)) });
    if (needsAuth(item)) list.push({ icon: KeyRound, label: t("row.reauth"), onSelect: () => setReauth(item) });
    if (item.status === Status.StatusCompleted) list.push({ icon: SquareArrowOutUpRight, label: t("row.open"), onSelect: run(() => Desktop.Open(item.id)) });
    if (canConvert && item.status === Status.StatusCompleted && /\.ts$/i.test(item.destination) && converting !== item.id)
      list.push({ icon: Film, label: t("row.convert"), onSelect: () => convert(item) });
    list.push({ icon: FolderOpen, label: t("row.reveal"), onSelect: run(() => Desktop.Reveal(item.id)) });
    list.push({ icon: Trash2, label: t("row.delete"), onSelect: () => setDeleting(item), danger: true });
    return list;
  };

  // Keyboard: ⌘/Ctrl+N add, ⌘/Ctrl+, settings, ⌘/Ctrl+F search, arrows move
  // the selection, Space pauses or resumes, Enter opens, Delete removes.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (modalOpen) return;
      const mod = isMac ? e.metaKey : e.ctrlKey;
      if (mod && e.key.toLowerCase() === "n") {
        e.preventDefault();
        setAdding({});
      } else if (mod && e.key === ",") {
        e.preventDefault();
        setSettingsOpen(true);
      } else if (mod && e.key.toLowerCase() === "f") {
        e.preventDefault();
        search.current?.focus();
      } else if (isTyping(e)) {
        if (e.key === "Escape") (e.target as HTMLElement).blur();
      } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        const i = rows.findIndex((r) => r.id === selected);
        const next = rows[Math.max(0, Math.min(rows.length - 1, i + (e.key === "ArrowDown" ? 1 : -1)))];
        if (next) setSelected(next.id);
      } else if (current && e.key === " ") {
        e.preventDefault();
        if (current.status === Status.StatusPaused) run(() => Desktop.Resume(current.id))();
        else if (current.status === Status.StatusRunning || current.status === Status.StatusQueued) run(() => Desktop.Pause(current.id))();
      } else if (current && (e.key === "Delete" || (isMac && e.key === "Backspace" && e.metaKey))) {
        setDeleting(current);
      } else if (current && e.key === "Enter") {
        run(() => (current.status === Status.StatusCompleted ? Desktop.Open(current.id) : Desktop.Reveal(current.id)))();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  const dropText = (e: React.DragEvent) => {
    const text = e.dataTransfer.getData("text/uri-list") || e.dataTransfer.getData("text/plain");
    const first = text.split(/\r?\n/).find((l) => l && !l.startsWith("#")) ?? "";
    return looksLikeCurl(text) ? text : looksLikeURL(first) ? first : "";
  };

  const contextItem = context ? items.get(context.id) : undefined;
  const closeContext = useCallback(() => setContext(null), []);
  const title = t(filters.find((f) => f.id === filter)!.label);

  return (
    <div
      className="shell"
      onDragEnter={(e) => {
        if (modalOpen || !e.dataTransfer.types.some((ty) => ty === "text/uri-list" || ty === "text/plain")) return;
        dragDepth.current++;
        setDragging(true);
      }}
      onDragLeave={() => {
        dragDepth.current = Math.max(0, dragDepth.current - 1);
        if (!dragDepth.current) setDragging(false);
      }}
      onDragOver={(e) => e.preventDefault()}
      onDrop={(e) => {
        dragDepth.current = 0;
        setDragging(false);
        const text = dropText(e);
        if (text && !modalOpen) {
          e.preventDefault();
          setAdding({ text });
        }
      }}
    >
      <aside className={`sidebar ${isMac ? "mac" : ""}`}>
        <div className="brand drag">
          <img className="logo" src="/nimget.png" alt="" />
          <span className="brand-name">NimGet</span>
        </div>
        <button className="primary block" onClick={() => setAdding({})} title={`${t("app.newDownload")} (${isMac ? "⌘" : "Ctrl+"}N)`}>
          <Plus size={16} strokeWidth={2.4} />
          {t("app.newDownload")}
        </button>
        <nav className="nav">
          {filters.map((f) => (
            <button key={f.id} className={`nav-item ${filter === f.id ? "on" : ""} ${f.id}`} onClick={() => setFilter(f.id)}>
              <f.icon size={16} />
              <span className="grow">{t(f.label)}</span>
              {counts[f.id] > 0 && <span className="count">{counts[f.id]}</span>}
            </button>
          ))}
        </nav>
        <div className="grow" />
        <SpeedCard
          running={all.filter((i) => i.status === Status.StatusRunning)}
          queued={all.filter((i) => i.status === Status.StatusQueued).length}
          rate={(id) => samples.current.get(id)?.bps ?? 0}
          bps={totalSpeed}
          history={history}
        />
        <button className="nav-item" onClick={() => setSettingsOpen(true)} title={`${t("nav.settings")} (${isMac ? "⌘" : "Ctrl+"},)`}>
          <SettingsIcon size={16} />
          <span className="grow">{t("nav.settings")}</span>
          {state && <span className="version">{/^\d/.test(state.version) ? `v${state.version}` : state.version}</span>}
        </button>
      </aside>

      <section className="main">
        <header className="topbar drag">
          <div className="title">
            <h1>{title}</h1>
            <span className="subtitle">{rows.length === 1 ? t("header.count1") : t("header.count", { n: rows.length })}</span>
          </div>
          <div className="grow" />
          <div className="search no-drag">
            <Search size={14} />
            <input ref={search} value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("header.search")} spellCheck={false} />
            {query && (
              <button className="icon ghost tiny" onClick={() => setQuery("")} aria-label={t("common.close")}>
                <X size={12} />
              </button>
            )}
          </div>
          <div className="tools no-drag">
            <button className="icon" title={t("header.resumeAll")} aria-label={t("header.resumeAll")} onClick={run(() => Desktop.ResumeAll())}>
              <Play size={16} />
            </button>
            <button className="icon" title={t("header.pauseAll")} aria-label={t("header.pauseAll")} onClick={run(() => Desktop.PauseAll())}>
              <Pause size={16} />
            </button>
            <Menu
              trigger={(open) => (
                <button className={`icon ${whenDone ? "armed" : ""} ${open ? "pressed" : ""}`} title={t("header.whenDone")} aria-label={t("header.whenDone")}>
                  {whenDone === "shutdown" ? <Power size={16} /> : <Moon size={16} />}
                  {whenDone && <span className="armed-label">{whenDone === "shutdown" ? t("whenDone.armedShutdown") : t("whenDone.armedSleep")}</span>}
                </button>
              )}
              entries={[
                { label: t("whenDone.nothing"), checked: whenDone === "", onSelect: () => chooseWhenDone("") },
                { icon: Moon, label: t("whenDone.sleep"), checked: whenDone === "sleep", onSelect: () => chooseWhenDone("sleep") },
                { icon: Power, label: t("whenDone.shutdown"), checked: whenDone === "shutdown", onSelect: () => chooseWhenDone("shutdown") },
              ]}
            />
            <Menu
              trigger={(open) => (
                <button className={`icon ${open ? "pressed" : ""}`} title={t("header.more")} aria-label={t("header.more")}>
                  <Ellipsis size={16} />
                </button>
              )}
              entries={[
                { icon: FolderInput, label: t("header.import"), onSelect: importQueue },
                { icon: FolderOutput, label: t("header.export"), onSelect: exportQueue },
              ]}
            />
          </div>
        </header>

        {state?.error && (
          <div className="banner danger">
            <CircleAlert size={16} />
            <span className="grow">{t("banner.serviceError", { error: state.error })}</span>
          </div>
        )}
        {state?.update && state.update.version !== dismissed && (
          <div className="banner info">
            <Sparkles size={16} />
            <span className="grow">{t("banner.update", { version: state.update.version })}</span>
            <button className="small" onClick={() => Browser.OpenURL(state.update!.url)}>
              {t("banner.releaseNotes")}
            </button>
            <button
              className="icon ghost tiny"
              title={t("banner.dismiss")}
              aria-label={t("banner.dismiss")}
              onClick={() => {
                const v = state.update!.version;
                setDismissed(v);
                try {
                  localStorage.setItem("dismissedUpdate", v);
                } catch {
                  // per-viewer convenience only
                }
              }}
            >
              <X size={14} />
            </button>
          </div>
        )}

        <div className="list" onMouseDown={(e) => e.target === e.currentTarget && setSelected(null)}>
          {rows.length === 0 ? (
            <Empty all={all.length} query={query} onAdd={() => setAdding({})} />
          ) : (
            rows.map((item) => (
              <Row
                key={item.id}
                item={item}
                bps={samples.current.get(item.id)?.bps ?? 0}
                selected={item.id === selected}
                converting={converting === item.id}
                actions={actions(item)}
                onSelect={() => setSelected(item.id)}
                onOpen={run(() => (item.status === Status.StatusCompleted ? Desktop.Open(item.id) : Desktop.Reveal(item.id)))}
                onContext={(x, y) => {
                  setSelected(item.id);
                  setContext({ x, y, id: item.id });
                }}
              />
            ))
          )}
        </div>
      </section>

      {context && contextItem && <ContextMenu x={context.x} y={context.y} entries={actions(contextItem)} onClose={closeContext} />}

      {dragging && (
        <div className="dropzone">
          <div className="dropzone-card">
            <ArrowDownToLine size={28} />
            <strong>{t("drop.title")}</strong>
            <span>{t("drop.body")}</span>
          </div>
        </div>
      )}

      <div className="floating">
        {clip && (
          <div className="card clip">
            <span className="card-icon">
              <Clipboard size={16} />
            </span>
            <div className="grow clip-body">
              <strong>{t("clip.title")}</strong>
              <span className="clip-text">{looksLikeCurl(clip) ? t("clip.curl") : clip}</span>
            </div>
            <button
              className="primary small"
              onClick={() => {
                setAdding({ text: clip });
                setClip(null);
              }}
            >
              <ArrowDownToLine size={14} />
              {t("clip.download")}
            </button>
            <button className="icon ghost tiny" onClick={() => setClip(null)} aria-label={t("banner.dismiss")} title={t("banner.dismiss")}>
              <X size={14} />
            </button>
          </div>
        )}
        {toast && (
          <div className={`card toast ${toast.kind}`} onClick={() => setToast(null)} role="status">
            <span className="card-icon">{toast.kind === "error" ? <CircleAlert size={16} /> : <CircleCheck size={16} />}</span>
            <span className="grow toast-text">{toast.text}</span>
          </div>
        )}
      </div>

      {countdown && (
        <CountdownDialog
          action={countdown.action}
          left={countdown.left}
          onCancel={() => {
            setCountdown(null);
            chooseWhenDone("");
          }}
        />
      )}
      {adding && state && <AddDialog prefill={adding} defaultDirectory={state.defaultDirectory} onClose={() => setAdding(null)} onError={fail} />}
      {settingsOpen && state && (
        <SettingsDialog
          state={state}
          onClose={() => setSettingsOpen(false)}
          onSaved={() => {
            Desktop.State().then(setState);
            Desktop.CanConvert().then(setCanConvert);
          }}
        />
      )}
      {deleting && <DeleteDialog item={items.get(deleting.id) ?? deleting} onClose={() => setDeleting(null)} onError={fail} />}
      {reauth && <ReauthDialog item={reauth} onClose={() => setReauth(null)} onError={fail} />}
    </div>
  );
}

function Row({
  item,
  bps,
  selected,
  converting,
  actions,
  onSelect,
  onOpen,
  onContext,
}: {
  item: Item;
  bps: number;
  selected: boolean;
  converting: boolean;
  actions: Action[];
  onSelect: () => void;
  onOpen: () => void;
  onContext: (x: number, y: number) => void;
}) {
  const { t } = useI18n();
  const name = baseName(item.destination);
  const kind = fileKind(name);
  const Icon = kindIcon[kind];
  const known = item.total > 0;
  const pct = known ? Math.min(100, (item.completed / item.total) * 100) : item.status === Status.StatusCompleted ? 100 : 0;
  // Two quick actions on the row; the rest are in the menu.
  const quick = actions.filter((a) => !a.danger).slice(0, 2);
  return (
    <div
      className={`row ${item.status} ${selected ? "selected" : ""}`}
      onMouseDown={onSelect}
      onDoubleClick={onOpen}
      onContextMenu={(e) => {
        e.preventDefault();
        onContext(e.clientX, e.clientY);
      }}
      title={item.url}
    >
      <div className={`kind ${kind}`}>
        <Icon size={18} strokeWidth={1.8} />
      </div>
      <div className="row-main">
        <div className="row-title">
          <span className="name">{name}</span>
          {item.priority === 1 && <span className="badge high">{t("priority.high")}</span>}
          {item.priority === -1 && <span className="badge low">{t("priority.low")}</span>}
        </div>
        {(item.status === Status.StatusRunning || item.status === Status.StatusPaused || (item.status !== Status.StatusCompleted && item.completed > 0)) && (
          <div className={`bar ${item.status} ${!known && item.status === Status.StatusRunning ? "indeterminate" : ""}`}>
            <i style={{ width: `${pct}%` }} />
          </div>
        )}
        <div className="row-meta">
          <span className={`status ${item.status}`}>
            <span className="dot" />
            {converting ? t("row.converting") : t(`status.${item.status}` as "status.queued")}
          </span>
          <Meta item={item} bps={bps} t={t} />
        </div>
      </div>
      <div className="row-side">
        <span className="pct">{item.status === Status.StatusCompleted ? (known ? bytes(item.total) : "") : known ? `${Math.floor(pct)}%` : ""}</span>
        <div className="row-actions" onMouseDown={(e) => e.stopPropagation()} onDoubleClick={(e) => e.stopPropagation()}>
          {quick.map((a) => (
            <button key={a.label} className="icon ghost" title={a.label} aria-label={a.label} onClick={a.onSelect}>
              {a.icon && <a.icon size={15} />}
            </button>
          ))}
          <button className="icon ghost" title={t("header.more")} aria-label={t("header.more")} onClick={(e) => onContext(e.clientX, e.clientY)}>
            <Ellipsis size={15} />
          </button>
        </div>
      </div>
    </div>
  );
}

/** SpeedCard is a compact status line when idle and a live readout while downloading. */
function SpeedCard({ running, queued, rate, bps, history }: { running: Item[]; queued: number; rate: (id: string) => number; bps: number; history: number[] }) {
  const { t } = useI18n();
  if (running.length === 0) {
    return (
      <div className="speed-card idle">
        <span className="speed-icon">{queued > 0 ? <Hourglass size={15} /> : <Gauge size={15} />}</span>
        <div className="speed-idle-text">
          <strong>{t("speed.idle")}</strong>
          <span>{queued > 0 ? t("speed.queued", { n: queued }) : t("speed.none")}</span>
        </div>
      </div>
    );
  }
  // Everything is done when the slowest download is; that needs every size and rate.
  const secs = running.map((i) => (i.total > 0 && rate(i.id) > 0 ? (i.total - i.completed) / rate(i.id) : NaN));
  const left = secs.every(Number.isFinite) ? eta(Math.max(...secs), 1, t) : "";
  const [value, unit] = bps > 0 ? speed(bps).split(" ") : ["", ""];
  return (
    <div className="speed-card">
      <div className="speed-top">
        <span className="speed-live">
          <span className="live-dot" />
          {t("speed.downloading", { n: running.length })}
        </span>
        {queued > 0 && <span className="speed-queued">{t("speed.queued", { n: queued })}</span>}
      </div>
      {bps > 0 ? (
        <div className="speed-value">
          {value}
          <span className="speed-unit">{unit}</span>
        </div>
      ) : (
        <div className="speed-value connecting">{t("speed.connecting")}</div>
      )}
      <Sparkline values={history} />
      {left && <div className="speed-eta">{t("row.left", { t: left })}</div>}
    </div>
  );
}

function Meta({ item, bps, t }: { item: Item; bps: number; t: T }) {
  if (item.status === Status.StatusFailed && item.error) return <span className="meta error-text">{item.error}</span>;
  if (item.status === Status.StatusCompleted) return null;
  const parts: string[] = [];
  const known = item.total > 0;
  if (known) parts.push(t("row.of", { done: bytes(item.completed), total: bytes(item.total) }));
  else if (item.completed > 0) parts.push(bytes(item.completed));
  if (item.status === Status.StatusRunning) {
    if (bps > 0) parts.push(speed(bps));
    const left = known ? eta(item.total - item.completed, bps, t) : "";
    if (left) parts.push(t("row.left", { t: left }));
  }
  return (
    <>
      {parts.map((p, i) => (
        <span key={i} className="meta">
          {p}
        </span>
      ))}
    </>
  );
}

function Empty({ all, query, onAdd }: { all: number; query: string; onAdd: () => void }) {
  const { t } = useI18n();
  if (all > 0) {
    return (
      <div className="empty">
        <div className="empty-icon quiet">{query ? <Search size={22} /> : <Inbox size={22} />}</div>
        <p className="empty-title">{query ? t("empty.search", { q: query }) : t("empty.filtered")}</p>
      </div>
    );
  }
  return (
    <div className="empty">
      <div className="empty-icon">
        <ArrowDownToLine size={28} strokeWidth={2} />
      </div>
      <p className="empty-title">{t("empty.title")}</p>
      <p className="empty-body">{t("empty.body")}</p>
      <button className="primary" onClick={onAdd}>
        <Plus size={16} strokeWidth={2.4} />
        {t("app.newDownload")}
      </button>
    </div>
  );
}

function ContextMenu({ x, y, entries, onClose }: { x: number; y: number; entries: Action[]; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ left: x, top: y });
  useEffect(() => {
    const el = ref.current;
    if (el) setPos({ left: Math.min(x, window.innerWidth - el.offsetWidth - 8), top: Math.min(y, window.innerHeight - el.offsetHeight - 8) });
    const close = (e: MouseEvent) => !el?.contains(e.target as Node) && onClose();
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("mousedown", close);
    window.addEventListener("keydown", onKey);
    window.addEventListener("blur", onClose);
    return () => {
      window.removeEventListener("mousedown", close);
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("blur", onClose);
    };
  }, [x, y, onClose]);
  return (
    <div ref={ref} className="menu floating-menu" style={pos} role="menu">
      {entries.map((e, i) => (
        <div key={e.label}>
          {e.danger && i > 0 && <div className="menu-sep" />}
          <button
            role="menuitem"
            className={`menu-item ${e.danger ? "destructive" : ""}`}
            onClick={() => {
              onClose();
              e.onSelect();
            }}
          >
            {e.icon && <e.icon size={15} />}
            <span className="grow">{e.label}</span>
          </button>
        </div>
      ))}
    </div>
  );
}
