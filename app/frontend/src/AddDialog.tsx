import { useEffect, useState } from "react";
import { Desktop, Request, message } from "./api";
import { joinPath, looksLikeCurl, looksLikeURL, parseSpeed } from "./format";

export type AddPrefill = { text?: string };

function hostOf(url: string): string {
  try {
    return new URL(url).hostname.toLowerCase();
  } catch {
    return "";
  }
}

type Props = {
  prefill: AddPrefill;
  defaultDirectory: string;
  onClose: () => void;
  onError: (err: unknown) => void;
};

/** AddDialog queues one download per URL line, or one from a cURL command. */
export default function AddDialog({ prefill, defaultDirectory, onClose, onError }: Props) {
  const [text, setText] = useState(prefill.text ?? "");
  const [headers, setHeaders] = useState<Record<string, string>>({});
  // Imported headers (cookies) are sent only to the host they came from.
  const [headerHost, setHeaderHost] = useState("");
  const [directory, setDirectory] = useState("");
  const [fileName, setFileName] = useState("");
  const [priority, setPriority] = useState(0);
  const [connections, setConnections] = useState("");
  const [limit, setLimit] = useState("");
  const [checksum, setChecksum] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  // A pasted cURL command becomes a URL plus its headers.
  useEffect(() => {
    if (!looksLikeCurl(text)) return;
    Desktop.ParseCurl(text)
      .then((req) => {
        setText(req.url);
        setHeaders((req.headers as Record<string, string>) ?? {});
        setHeaderHost(hostOf(req.url));
        setError("");
      })
      .catch((err) => setError(message(err)));
  }, [text]);

  useEffect(() => {
    Desktop.Settings()
      .then((s) => {
        if (s.desktop.askDirectory) pick();
      })
      .catch(() => {});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const lines = text.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
  const [expanded, setExpanded] = useState<string[] | null>(null);
  const [batchError, setBatchError] = useState("");
  const isPattern = lines.length === 1 && /\[[^\]]+-[^\]]+\]|\{[^}]*,[^}]*\}/.test(lines[0]);
  // A single line with [1-10] or {a,b} groups is a batch pattern; a stale
  // expansion must not overwrite the result for newer text.
  useEffect(() => {
    setExpanded(null);
    setBatchError("");
    if (!isPattern) return;
    let current = true;
    Desktop.ExpandBatch(lines[0])
      .then((list) => current && setExpanded(list ?? []))
      .catch((err) => current && setBatchError(message(err)));
    return () => {
      current = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text]);
  const urls = expanded ?? lines;
  const valid = urls.length > 0 && urls.every(looksLikeURL) && (!isPattern || expanded !== null);

  async function pick() {
    try {
      const dir = await Desktop.PickDirectory();
      if (dir) setDirectory(dir);
    } catch (err) {
      setError(message(err));
    }
  }

  async function submit() {
    setBusy(true);
    setError("");
    try {
      const base = {
        priority,
        connections: connections ? parseInt(connections, 10) : 0,
        speedLimit: parseSpeed(limit),
        checksum: checksum.trim(),
      };
      for (const url of urls) {
        const req = new Request({ ...base, url });
        if (Object.keys(headers).length && hostOf(url) === headerHost) req.headers = headers;
        if (fileName.trim() && urls.length === 1) {
          req.destination = joinPath(directory || defaultDirectory, fileName.trim());
        } else if (directory) {
          req.directory = directory;
        }
        await Desktop.Add(req);
      }
      onClose();
    } catch (err) {
      setError(message(err));
      onError(err);
    } finally {
      setBusy(false);
    }
  }

  const headerCount = Object.keys(headers).length;
  const otherHosts = headerCount > 0 && urls.some((u) => hostOf(u) !== headerHost);
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>Add download</h2>
        <label>
          URLs, one per line, a batch pattern such as img[001-120].jpg, or a “Copy as cURL” command
          <textarea rows={4} value={text} onChange={(e) => setText(e.target.value)} placeholder="https://example.com/file.zip" autoFocus />
        </label>
        {batchError && <p className="error">Batch pattern: {batchError}</p>}
        {expanded && (
          <p className="note">
            Batch pattern: {expanded.length} URLs, from {expanded[0]} to {expanded[expanded.length - 1]}.
          </p>
        )}
        {headerCount > 0 && (
          <p className="note">
            Imported {headerCount} request header{headerCount > 1 ? "s" : ""} ({Object.keys(headers).join(", ")}) for {headerHost}
            {otherHosts ? "; URLs on other hosts are added without them" : ""}.{" "}
            <button className="link" onClick={() => setHeaders({})}>Remove</button>
          </p>
        )}
        <div className="row">
          <label className="grow">
            Folder
            <input readOnly value={directory || "Category folder in " + defaultDirectory} />
          </label>
          <button onClick={pick}>Choose…</button>
          {directory && <button onClick={() => setDirectory("")}>Reset</button>}
        </div>
        {urls.length <= 1 && (
          <label>
            File name (optional; the server's name is used otherwise)
            <input value={fileName} onChange={(e) => setFileName(e.target.value)} />
          </label>
        )}
        <div className="row">
          <label>
            Priority
            <select value={priority} onChange={(e) => setPriority(parseInt(e.target.value, 10))}>
              <option value={1}>High</option>
              <option value={0}>Normal</option>
              <option value={-1}>Low</option>
            </select>
          </label>
          <label>
            Connections
            <input type="number" min={1} max={32} placeholder="default" value={connections} onChange={(e) => setConnections(e.target.value)} />
          </label>
          <label>
            Speed limit
            <input placeholder="none, or 2M" value={limit} onChange={(e) => setLimit(e.target.value)} />
          </label>
        </div>
        <label>
          Checksum (optional)
          <input placeholder="sha256:…" value={checksum} onChange={(e) => setChecksum(e.target.value)} />
        </label>
        {error && <p className="error">{error}</p>}
        <div className="actions">
          <button onClick={onClose}>Cancel</button>
          <button className="primary" disabled={!valid || busy} onClick={submit}>
            {urls.length > 1 ? `Add ${urls.length} downloads` : "Download"}
          </button>
        </div>
      </div>
    </div>
  );
}
