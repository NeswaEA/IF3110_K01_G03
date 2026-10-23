import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// Pages are rendered by the Go server; Vite only bundles one script per page.
// The manifest lets the server resolve each entry name to its hashed files.
export default defineConfig({
  plugins: [react()],
  build: {
    manifest: true,
    rolldownOptions: {
      input: {
        layout: "src/pages/layout.ts",
        connections: "src/pages/connections.tsx",
        migrations: "src/pages/migrations.tsx",
      },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
    css: false,
    env: { VITE_API_BASE_URL: "http://localhost/api/v1" },
  },
});
