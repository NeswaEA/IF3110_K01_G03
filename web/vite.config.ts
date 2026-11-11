import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

const api = process.env.VITE_DEV_API ?? "http://localhost:8080";

export default defineConfig({
  // the router plugin must run before React to generate src/routeTree.gen.ts
  plugins: [tanstackRouter({ target: "react", autoCodeSplitting: true }), react(), tailwindcss()],
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  server: {
    proxy: {
      // The API checks Origin against APP_ORIGIN on mutations; present the dev
      // server's requests as coming from the API origin.
      "/api": { target: api, configure: (proxy) => proxy.on("proxyReq", (req) => req.setHeader("origin", api)) },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
    css: false,
    env: { VITE_API_BASE_URL: "http://localhost/api/v1" },
  },
});
