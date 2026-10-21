import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// Dev server proxies /api to the Go backend (single origin in production).
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: { "/api": { target: process.env.VITE_DEV_PROXY_TARGET ?? "http://localhost:8080", changeOrigin: false } },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
    css: false,
    env: { VITE_API_BASE_URL: "http://localhost/api/v1" },
  },
});
