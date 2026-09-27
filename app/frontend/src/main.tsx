import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import { applyTheme, loadTheme } from "./theme";
import "./styles.css";

applyTheme(loadTheme());
if (navigator.userAgent.includes("Mac")) document.documentElement.classList.add("mac");

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
