import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build lands inside the Go package so `go:embed` ships it in the binary.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/analytics/web/dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 1200,
  },
  server: {
    port: 5173,
    proxy: { "/api": "http://localhost:8090" },
  },
});
