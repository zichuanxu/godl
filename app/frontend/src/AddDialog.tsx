import { useEffect, useState } from "react";
import { ArrowDownToLine, ChevronRight, CircleAlert, Cookie, Folder, Layers, Link2, X } from "lucide-react";
import { Desktop, Request, message } from "./api";
import { baseName, joinPath, looksLikeCurl, looksLikeURL, parseSpeed } from "./format";
import { useI18n } from "./i18n";
import { Field, Modal, Segmented } from "./ui";

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
  const { t } = useI18n();
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
  const [showOptions, setShowOptions] = useState(false);
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
  const optionsSet = priority !== 0 || !!connections || !!limit || !!checksum;
  return (
    <Modal
      icon={ArrowDownToLine}
      title={t("add.title")}
      onClose={onClose}
      footer={
        <>
          <button onClick={onClose}>{t("common.cancel")}</button>
          <button className="primary" disabled={!valid || busy} onClick={submit}>
            <ArrowDownToLine size={14} />
            {urls.length > 1 ? t("add.submitMany", { n: urls.length }) : t("add.submit")}
          </button>
        </>
      }
    >
      <Field label={t("add.urls")} hint={t("add.urlsHint")}>
        <div className="input-icon top">
          <Link2 size={15} />
          <textarea
            className="urls"
            rows={3}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && valid && !busy) submit();
            }}
            placeholder="https://example.com/file.zip"
            autoFocus
            spellCheck={false}
          />
        </div>
      </Field>

      {batchError && (
        <div className="chip danger">
          <CircleAlert size={14} />
          {t("add.batchError", { error: batchError })}
        </div>
      )}
      {expanded && expanded.length > 0 && (
        <div className="chip">
          <Layers size={14} />
          <span className="ellipsis">{t("add.batch", { n: expanded.length, first: baseName(expanded[0]), last: baseName(expanded[expanded.length - 1]) })}</span>
        </div>
      )}
      {headerCount > 0 && (
        <div className="chip">
          <Cookie size={14} />
          <span className="grow ellipsis" title={Object.keys(headers).join(", ")}>
            {headerCount === 1 ? t("add.headers1", { host: headerHost }) : t("add.headers", { n: headerCount, host: headerHost })}
            {otherHosts ? ` ${t("add.headersOther")}` : ""}
          </span>
          <button className="icon ghost tiny" onClick={() => setHeaders({})} title={t("common.remove")} aria-label={t("common.remove")}>
            <X size={12} />
          </button>
        </div>
      )}

      <Field label={t("add.saveTo")} plain>
        <div className="folder-picker">
          <Folder size={15} />
          <span className={`grow ellipsis ${directory ? "" : "muted"}`} title={directory || defaultDirectory}>
            {directory || t("add.categoryFolder", { dir: defaultDirectory })}
          </span>
          {directory && (
            <button className="small ghost" onClick={() => setDirectory("")}>
              {t("add.reset")}
            </button>
          )}
          <button className="small" onClick={pick}>
            {t("add.choose")}
          </button>
        </div>
      </Field>

      {urls.length <= 1 && (
        <Field label={t("add.fileName")}>
          <input value={fileName} onChange={(e) => setFileName(e.target.value)} placeholder={t("add.fileNameHint")} spellCheck={false} />
        </Field>
      )}

      <button className={`disclosure ${showOptions ? "open" : ""}`} onClick={() => setShowOptions(!showOptions)}>
        <ChevronRight size={14} />
        {t("add.options")}
        {optionsSet && !showOptions && <span className="dot-accent" />}
      </button>
      {showOptions && (
        <div className="options">
          <Field label={t("add.priority")} plain>
            <Segmented
              value={priority}
              onChange={setPriority}
              options={[
                { value: -1, label: t("priority.low") },
                { value: 0, label: t("priority.normal") },
                { value: 1, label: t("priority.high") },
              ]}
            />
          </Field>
          <div className="grid2">
            <Field label={t("add.connections")}>
              <input type="number" min={1} max={32} placeholder={t("add.default")} value={connections} onChange={(e) => setConnections(e.target.value)} />
            </Field>
            <Field label={t("add.speedLimit")}>
              <input placeholder={t("add.speedHint")} value={limit} onChange={(e) => setLimit(e.target.value)} />
            </Field>
          </div>
          <Field label={t("add.checksum")}>
            <input className="mono" placeholder="sha256:…" value={checksum} onChange={(e) => setChecksum(e.target.value)} spellCheck={false} />
          </Field>
        </div>
      )}

      {error && (
        <p className="form-error">
          <CircleAlert size={14} />
          {error}
        </p>
      )}
    </Modal>
  );
}
