import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { adoptSessionKey } from "./api/session";
import { followDesktopPreferences } from "./desktop";
import { preloadPage } from "./Shell";
import "./styles.css";

adoptSessionKey();
followDesktopPreferences();

// A page chunk that fails to load is asked for again when the page renders;
// a second failure is shown by the shell's page boundary. The other pages'
// chunks load once this page has settled (Shell).
await preloadPage(window.location.pathname).catch(() => {});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
