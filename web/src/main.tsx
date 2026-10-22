import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/inter";
import "./styles.css";
import "./analyst.css";
import { App } from "./App";
import { AppProvider } from "./lib/state";
import { ToastProvider } from "./components/Toast";
import { captureTitles, TitleTips } from "./components/InfoTip";
import { prefs } from "./lib/prefs";
import { setTimeDisplay } from "./lib/time";

// Before the first render, so no title ever reaches the page as a native tooltip.
captureTitles();
// Times in the zone and clock of Settings → Preferences, from the local copy.
setTimeDisplay(prefs());

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AppProvider>
      <ToastProvider>
        <App />
        <TitleTips />
      </ToastProvider>
    </AppProvider>
  </StrictMode>,
);
