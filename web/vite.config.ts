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
        login: "src/pages/login.ts",
        users: "src/pages/users.ts",
        groups: "src/pages/groups.ts",
        members: "src/pages/members.ts",
        grants: "src/pages/grants.ts",
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
