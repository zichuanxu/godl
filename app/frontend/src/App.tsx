import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Events } from "@wailsio/runtime";
import { Desktop, EventType, Item, State, Status, message } from "./api";
import AddDialog, { type AddPrefill } from "./AddDialog";
import SettingsDialog from "./SettingsDialog";
import Sparkline from "./Sparkline";
import { baseName, bytes, eta, looksLikeCurl, looksLikeURL, speed } from "./format";

type Sample = { at: number; done: number; bps: number };

const statusLabel: Record<string, string> = {
  queued: "Queued",
  running: "Downloading",
  paused: "Paused",
  completed: "Completed",
  failed: "Failed",
};

const priorityLabel: Record<number, string> = { [-1]: "Low", 0: "", 1: "High" };

export default function App() {
  const [state, setState] = useState<State | null>(null);
  const [items, setItems] = useState<Map<string, Item>>(new Map());
  const [selected, setSelected] = useState<string | null>(null);
  const [adding, setAdding] = useState<AddPrefill | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [deleting, setDeleting] = useState<Item | null>(null);
  const [reauth, setReauth] = useState<Item | null>(null);
  const [clip, setClip] = useState<string | null>(null);
  const [toast, setToast] = useState("");
  const [history, setHistory] = useState<number[]>([]);
  const [whenDone, setWhenDone] = useState("");
  const [canConvert, setCanConvert] = useState(false);
  const [converting, setConverting] = useState(false);
  const [countdown, setCountdown] = useState<{ action: string; left: number } | null>(null);
  const samples = useRef(new Map<string, Sample>());
  const [, setTick] = useState(0);

  const fail = useCallback((err: unknown) => setToast(message(err)), []);

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
    const timer = window.setInterval(() => {
      const now = performance.now();
      let total = 0;
      for (const [id, s] of samples.current) {
        if (now - s.at > 3000) samples.current.set(id, { ...s, bps: 0 });
        total += s.bps;
      }
      setHistory((h) => [...h.slice(-59), total]);
      setTick((t) => t + 1);
    }, 1000);
    return () => {
      offDownload();
      offResync();
      offStopped();
      offClip();
      offDone();
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
    const t = window.setTimeout(() => setCountdown({ ...countdown, left: countdown.left - 1 }), 1000);
    return () => window.clearTimeout(t);
  }, [countdown, fail]);

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
      if (res.failed?.length) setToast(`Added ${res.added}; not added:\n${res.failed.join("\n")}`);
    } catch (err) {
      fail(err);
    }
  };

  useEffect(() => {
    if (!toast) return;
    const t = window.setTimeout(() => setToast(""), 6000);
    return () => window.clearTimeout(t);
  }, [toast]);

  // RFC 3339 strings with trimmed fractions do not sort as text.
  const rows = useMemo(() => [...items.values()].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt)), [items]);
  const current = selected ? items.get(selected) : undefined;
  const totalSpeed = history.length ? history[history.length - 1] : 0;
  const active = rows.filter((i) => i.status === Status.StatusRunning).length;

  const run = (fn: () => Promise<unknown>) => () => {
    fn().catch(fail);
  };

  const onDrop = (e: React.DragEvent) => {
    const text = e.dataTransfer.getData("text/uri-list") || e.dataTransfer.getData("text/plain");
    const first = text.split(/\r?\n/).find((l) => l && !l.startsWith("#")) ?? "";
    if (looksLikeURL(first) || looksLikeCurl(text)) {
      e.preventDefault();
      setAdding({ text: looksLikeCurl(text) ? text : first });
    }
  };

  const needsAuth = (item?: Item) => !!item && item.status === Status.StatusFailed && /HTTP (401|403)\b/.test(item.error ?? "");

  return (
    <div className="app" onDragOver={(e) => e.preventDefault()} onDrop={onDrop}>
      {state?.error && (
        <div className="banner">
          The download service could not start: {state.error}. If <code>godl service</code> is running, stop it and reopen godl.
        </div>
      )}
      <header className="toolbar">
        <button className="primary" onClick={() => setAdding({})}>Add</button>
        <span className="sep" />
        <button disabled={!current || current.status !== Status.StatusPaused} onClick={run(() => Desktop.Resume(current!.id))}>Resume</button>
        <button disabled={!current || (current.status !== Status.StatusRunning && current.status !== Status.StatusQueued)} onClick={run(() => Desktop.Pause(current!.id))}>Pause</button>
        <button disabled={!current || current.status !== Status.StatusFailed} onClick={run(() => Desktop.Retry(current!.id))}>Retry</button>
        <button disabled={!current} onClick={() => setDeleting(current!)}>Delete</button>
        <span className="sep" />
        <button disabled={!current || current.status !== Status.StatusCompleted} onClick={run(() => Desktop.Open(current!.id))}>Open</button>
        <button disabled={!current} onClick={run(() => Desktop.Reveal(current!.id))}>Show in folder</button>
        {needsAuth(current) && <button onClick={() => setReauth(current!)}>Re-authenticate</button>}
        {canConvert && current?.status === Status.StatusCompleted && /\.ts$/i.test(current.destination) && (
          <button
            disabled={converting}
            onClick={() => {
              setConverting(true);
              Desktop.ConvertToMP4(current.id)
                .then((out) => setToast(`Saved ${baseName(out)}`))
                .catch(fail)
                .finally(() => setConverting(false));
            }}
          >
            {converting ? "Converting…" : "Convert to MP4"}
          </button>
        )}
        <span className="grow" />
        <label className="inline" title="What to do once no download is queued or running">
          When done
          <select value={whenDone} onChange={(e) => chooseWhenDone(e.target.value)}>
            <option value="">Nothing</option>
            <option value="sleep">Sleep</option>
            <option value="shutdown">Shut down</option>
          </select>
        </label>
        <button onClick={importQueue}>Import</button>
        <button onClick={run(() => Desktop.ExportQueue())}>Export</button>
        <button onClick={run(() => Desktop.PauseAll())}>Pause all</button>
        <button onClick={run(() => Desktop.ResumeAll())}>Resume all</button>
        <button onClick={() => setSettingsOpen(true)}>Settings</button>
      </header>

      <main className="list">
        {rows.length === 0 ? (
          <div className="empty">
            <p>No downloads yet.</p>
            <p>Click Add, drop a link here, or copy a link or a “Copy as cURL” command.</p>
          </div>
        ) : (
          <table>
            <thead>
              <tr>
                <th className="name">Name</th>
                <th className="num">Size</th>
                <th className="progress">Progress</th>
                <th className="num">Speed</th>
                <th className="num">Time left</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((item) => {
                const bps = samples.current.get(item.id)?.bps ?? 0;
                const pct = item.total > 0 ? Math.min(100, (item.completed / item.total) * 100) : item.status === Status.StatusCompleted ? 100 : 0;
                return (
                  <tr
                    key={item.id}
                    className={item.id === selected ? "selected" : ""}
                    onClick={() => setSelected(item.id)}
                    onDoubleClick={run(() => (item.status === Status.StatusCompleted ? Desktop.Open(item.id) : Desktop.Reveal(item.id)))}
                    title={item.url}
                  >
                    <td className="name">
                      {baseName(item.destination)}
                      {priorityLabel[item.priority] && <span className={`badge p${item.priority}`}>{priorityLabel[item.priority]}</span>}
                    </td>
                    <td className="num">{item.total > 0 ? bytes(item.total) : ""}</td>
                    <td className="progress">
                      <div className={`bar ${item.status}`}>
                        <div style={{ width: `${pct}%` }} />
                      </div>
                      <span className="pct">{item.total > 0 ? `${pct.toFixed(1)}%` : bytes(item.completed)}</span>
                    </td>
                    <td className="num">{item.status === Status.StatusRunning ? speed(bps) : ""}</td>
                    <td className="num">{item.status === Status.StatusRunning ? eta(item.total - item.completed, bps) : ""}</td>
                    <td className={`status ${item.status}`} title={item.error}>
                      {statusLabel[item.status] ?? item.status}
                      {item.error ? ` – ${item.error}` : ""}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </main>

      <footer className="statusbar">
        <span>{active} active · {rows.length} total</span>
        <span className="grow" />
        <Sparkline values={history} />
        <span className="speed">{speed(totalSpeed) || "idle"}</span>
        {state && <span className="version">godl {state.version}</span>}
      </footer>

      {clip && (
        <div className="clip">
          <div className="clip-text">{looksLikeCurl(clip) ? "Copied a cURL command" : clip}</div>
          <button className="primary" onClick={() => { setAdding({ text: clip }); setClip(null); }}>Download</button>
          <button onClick={() => setClip(null)}>Dismiss</button>
        </div>
      )}
      {toast && <div className="toast" onClick={() => setToast("")}>{toast}</div>}
      {countdown && (
        <div className="modal-backdrop">
          <div className="modal small">
            <h2>All downloads are done</h2>
            <p>
              {countdown.action === "shutdown" ? "Shutting down" : "Going to sleep"} in {countdown.left} s.
            </p>
            <div className="actions">
              <button className="primary" onClick={() => { setCountdown(null); chooseWhenDone(""); }}>Cancel</button>
            </div>
          </div>
        </div>
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

function DeleteDialog({ item, onClose, onError }: { item: Item; onClose: () => void; onError: (e: unknown) => void }) {
  const [files, setFiles] = useState(false);
  const confirm = async () => {
    try {
      // The status may have changed since the dialog opened; pausing a
      // stopped download is a harmless conflict.
      await Desktop.Pause(item.id).catch(() => {});
      // A paused runner may still be checkpointing; retry briefly.
      for (let i = 0; ; i++) {
        try {
          await Desktop.Delete(item.id, files);
          break;
        } catch (err) {
          if (i >= 20 || !/active/.test(message(err))) throw err;
          await new Promise((r) => setTimeout(r, 150));
        }
      }
      onClose();
    } catch (err) {
      onError(err);
      onClose();
    }
  };
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal small" onClick={(e) => e.stopPropagation()}>
        <h2>Delete download?</h2>
        <p className="path">{baseName(item.destination)}</p>
        <label className="check">
          <input type="checkbox" checked={files} onChange={(e) => setFiles(e.target.checked)} />
          {item.status === Status.StatusCompleted ? "Also delete the file" : "Also delete the partial data"}
        </label>
        <div className="actions">
          <button onClick={onClose}>Cancel</button>
          <button className="danger" onClick={confirm}>Delete</button>
        </div>
      </div>
    </div>
  );
}

function ReauthDialog({ item, onClose, onError }: { item: Item; onClose: () => void; onError: (e: unknown) => void }) {
  const [text, setText] = useState("");
  const submit = async () => {
    try {
      await Desktop.Reauthenticate(item.id, text);
      onClose();
    } catch (err) {
      onError(err);
    }
  };
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>Re-authenticate</h2>
        <p>
          The server refused <b>{baseName(item.destination)}</b>. In your browser, open the developer tools, find the request, choose “Copy as cURL”, and paste it here.
        </p>
        <textarea rows={6} value={text} onChange={(e) => setText(e.target.value)} placeholder="curl 'https://…' -H 'cookie: …'" autoFocus />
        <div className="actions">
          <button onClick={onClose}>Cancel</button>
          <button className="primary" disabled={!looksLikeCurl(text)} onClick={submit}>Retry with these credentials</button>
        </div>
      </div>
    </div>
  );
}
