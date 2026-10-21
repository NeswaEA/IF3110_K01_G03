import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./app/App";

async function enableMocks() {
  // import.meta.env.DEV is false in production builds, so MSW is tree-shaken out.
  if (import.meta.env.DEV && import.meta.env.VITE_ENABLE_MOCKS === "true") {
    const { worker } = await import("./mocks/browser");
    await worker.start({ onUnhandledFrame: "error" });
  }
}

enableMocks().then(() => {
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
});
