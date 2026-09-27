import type { T } from "./i18n";

const units = ["B", "KiB", "MiB", "GiB", "TiB"];

export function bytes(n: number): string {
  if (!(n >= 0)) return "?";
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return i === 0 ? `${n} B` : `${n.toFixed(n < 10 ? 2 : 1)} ${units[i]}`;
}

export function speed(bps: number): string {
  return bps > 0 ? `${bytes(Math.round(bps))}/s` : "";
}

export function eta(remaining: number, bps: number, t: T): string {
  if (!(bps > 0) || !(remaining > 0)) return "";
  let s = Math.round(remaining / bps);
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  s -= m * 60;
  if (h > 99) return t("eta.long");
  return h > 0 ? t("eta.h", { h, m }) : m > 0 ? t("eta.m", { m, s }) : t("eta.s", { s });
}

export type FileKind = "video" | "audio" | "archive" | "image" | "document" | "program" | "file";

const kinds: [FileKind, RegExp][] = [
  ["video", /\.(mp4|mkv|mov|avi|webm|m4v|ts|flv|wmv)$/i],
  ["audio", /\.(mp3|flac|wav|aac|m4a|ogg|opus)$/i],
  ["archive", /\.(zip|rar|7z|tar|gz|tgz|bz2|xz|zst|iso)$/i],
  ["image", /\.(png|jpe?g|gif|webp|svg|bmp|heic|avif)$/i],
  ["document", /\.(pdf|docx?|xlsx?|pptx?|txt|md|epub|csv)$/i],
  ["program", /\.(exe|msi|dmg|pkg|deb|rpm|appimage|apk)$/i],
];

/** fileKind groups a file name by extension for its icon. */
export function fileKind(name: string): FileKind {
  return kinds.find(([, re]) => re.test(name))?.[0] ?? "file";
}

/** baseName handles both / and \ separators. */
export function baseName(path: string): string {
  const i = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  return i >= 0 ? path.slice(i + 1) : path;
}

export function joinPath(dir: string, name: string): string {
  const sep = dir.includes("\\") && !dir.includes("/") ? "\\" : "/";
  return dir.endsWith(sep) ? dir + name : dir + sep + name;
}

/** parseSpeed reads "500K", "2M", "1.5 MB/s", or a byte count; "" is 0. */
export function parseSpeed(text: string): number {
  const m = text.trim().toUpperCase().replace(/\/S$/, "").replace(/I?B$/, "").match(/^(\d+(?:\.\d+)?)\s*([KMG]?)$/);
  if (!text.trim()) return 0;
  if (!m) throw new Error(`Invalid speed "${text}"; use for example 500K or 2M`);
  const shift = { "": 0, K: 10, M: 20, G: 30 }[m[2] as "" | "K" | "M" | "G"];
  return Math.round(parseFloat(m[1]) * 2 ** shift);
}

export function speedText(bps: number): string {
  if (!bps) return "";
  for (const [suffix, shift] of [["G", 30], ["M", 20], ["K", 10]] as const) {
    if (bps >= 2 ** shift && bps % 2 ** shift === 0) return `${bps / 2 ** shift}${suffix}`;
  }
  return String(bps);
}

export function looksLikeURL(text: string): boolean {
  return /^https?:\/\/\S+$/i.test(text.trim());
}

export function looksLikeCurl(text: string): boolean {
  return /^curl(\.exe)?\s/i.test(text.trim());
}
