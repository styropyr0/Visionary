import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/inter";
import "./styles.css";
import "./analyst.css";
import { App } from "./App";
import { AppProvider } from "./lib/state";
import { ToastProvider } from "./components/Toast";
import { TitleTips } from "./components/InfoTip";

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
