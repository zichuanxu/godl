import { useState } from "react";
import { KeyRound, Moon, Power, RotateCw, Trash2 } from "lucide-react";
import { Desktop, Item, Status, message } from "./api";
import { baseName, looksLikeCurl } from "./format";
import { useI18n } from "./i18n";
import { Modal } from "./ui";

export function DeleteDialog({ item, onClose, onError }: { item: Item; onClose: () => void; onError: (e: unknown) => void }) {
  const { t } = useI18n();
  const [files, setFiles] = useState(false);
  const [busy, setBusy] = useState(false);
  const confirm = async () => {
    setBusy(true);
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
    } catch (err) {
      onError(err);
    }
    onClose();
  };
  return (
    <Modal
      icon={Trash2}
      tone="danger"
      size="small"
      title={t("delete.title")}
      onClose={onClose}
      footer={
        <>
          <button onClick={onClose}>{t("common.cancel")}</button>
          <button className="danger" disabled={busy} onClick={confirm} autoFocus>
            <Trash2 size={14} />
            {t("delete.confirm")}
          </button>
        </>
      }
    >
      <p className="filename">{baseName(item.destination)}</p>
      <label className="check">
        <input type="checkbox" checked={files} onChange={(e) => setFiles(e.target.checked)} />
        {item.status === Status.StatusCompleted ? t("delete.file") : t("delete.partial")}
      </label>
    </Modal>
  );
}

export function ReauthDialog({ item, onClose, onError }: { item: Item; onClose: () => void; onError: (e: unknown) => void }) {
  const { t } = useI18n();
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
    <Modal
      icon={KeyRound}
      tone="warn"
      title={t("reauth.title")}
      onClose={onClose}
      footer={
        <>
          <button onClick={onClose}>{t("common.cancel")}</button>
          <button className="primary" disabled={!looksLikeCurl(text)} onClick={submit}>
            <RotateCw size={14} />
            {t("reauth.submit")}
          </button>
        </>
      }
    >
      <p className="muted">{t("reauth.body", { name: baseName(item.destination) })}</p>
      <textarea className="mono" rows={6} value={text} onChange={(e) => setText(e.target.value)} placeholder="curl 'https://…' -H 'cookie: …'" autoFocus spellCheck={false} />
    </Modal>
  );
}

export function CountdownDialog({ action, left, onCancel }: { action: string; left: number; onCancel: () => void }) {
  const { t } = useI18n();
  const shutdown = action === "shutdown";
  return (
    <Modal
      icon={shutdown ? Power : Moon}
      tone="warn"
      size="small"
      title={t("countdown.title")}
      onClose={onCancel}
      footer={
        <button className="primary" onClick={onCancel} autoFocus>
          {t("common.cancel")}
        </button>
      }
    >
      <div className="countdown">
        <span className="countdown-num">{left}</span>
        <p className="muted">{shutdown ? t("countdown.shutdown", { n: left }) : t("countdown.sleep", { n: left })}</p>
      </div>
    </Modal>
  );
}
