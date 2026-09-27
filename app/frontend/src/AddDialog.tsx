import { useEffect, useState } from "react";
import { Desktop, Request, message } from "./api";
import { joinPath, looksLikeCurl, looksLikeURL, parseSpeed } from "./format";

export type AddPrefill = { text?: string };

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

  const urls = text.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
  const valid = urls.length > 0 && urls.every(looksLikeURL);

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
        headers: Object.keys(headers).length ? headers : undefined,
      };
      for (const url of urls) {
        const req = new Request({ ...base, url });
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
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>Add download</h2>
        <label>
          URLs, one per line, or a “Copy as cURL” command
          <textarea rows={4} value={text} onChange={(e) => setText(e.target.value)} placeholder="https://example.com/file.zip" autoFocus />
        </label>
        {headerCount > 0 && (
          <p className="note">
            Imported {headerCount} request header{headerCount > 1 ? "s" : ""} ({Object.keys(headers).join(", ")}).{" "}
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
