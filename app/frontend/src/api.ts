// Re-exports of the generated bindings under short names.
export { Desktop, State } from "../bindings/github.com/zichuanxu/nimget/app";
export { Event, EventType, Item, Patch, Request, Status } from "../bindings/github.com/zichuanxu/nimget/internal/download";
export { Desktop as DesktopPrefs, Schedule, Settings, Site, SpeedRule } from "../bindings/github.com/zichuanxu/nimget/internal/settings";
export { Config as ProxyConfig, Mode as ProxyMode } from "../bindings/github.com/zichuanxu/nimget/internal/netproxy";

/** message extracts a readable error from a rejected binding call. */
export function message(err: unknown): string {
  if (err instanceof Error) return err.message;
  if (typeof err === "object" && err && "message" in err) return String((err as { message: unknown }).message);
  return String(err);
}
