// The appearance is a per-machine preference, so it lives in localStorage
// rather than in the service settings.
export type Theme = "system" | "light" | "dark";

export function loadTheme(): Theme {
  try {
    const v = localStorage.getItem("theme");
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}

export function saveTheme(theme: Theme) {
  try {
    localStorage.setItem("theme", theme);
  } catch {
    // unavailable storage: the choice lasts until the window reloads
  }
}

export function applyTheme(theme: Theme) {
  if (theme === "system") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = theme;
}
