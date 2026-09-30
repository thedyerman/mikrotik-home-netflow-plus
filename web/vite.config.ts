import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In development the Go server runs on :8080 and Vite proxies the API to it.
// Set NFP_DEV_API to proxy to another collector, e.g. http://192.168.88.10:8080.
const api = process.env.NFP_DEV_API ?? "http://127.0.0.1:8080";

export default defineConfig({
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true, chunkSizeWarningLimit: 900 },
  server: {
    proxy: {
      "/api": { target: api, ws: true },
      "/healthz": api,
    },
  },
});
