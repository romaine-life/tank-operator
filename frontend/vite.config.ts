import { defineConfig } from "vite";
import path from "node:path";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "src"),
    },
  },
  server: {
    proxy: {
      "/api": "http://localhost:8000",
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // ghostty-web embeds its WASM in a lazy terminal chunk. Keep the limit
    // above that expected payload while still catching oversized app chunks.
    chunkSizeWarningLimit: 700,
    rollupOptions: {
      output: {
        manualChunks(id) {
          const normalizedId = id.split(path.sep).join("/");
          if (!normalizedId.includes("/node_modules/")) return;
          if (
            normalizedId.includes("/node_modules/react/") ||
            normalizedId.includes("/node_modules/react-dom/") ||
            normalizedId.includes("/node_modules/scheduler/")
          ) {
            return "vendor-react";
          }
          if (
            normalizedId.includes("/node_modules/lucide-react/") ||
            normalizedId.includes("/node_modules/lucide/")
          ) {
            return "vendor-icons";
          }
          if (
            normalizedId.includes("/node_modules/react-virtuoso/") ||
            normalizedId.includes("/node_modules/@virtuoso.dev/")
          ) {
            return "vendor-virtual-list";
          }
          if (normalizedId.includes("/node_modules/streamdown/")) {
            return "vendor-markdown";
          }
          if (
            normalizedId.includes("/node_modules/radix-ui/") ||
            normalizedId.includes("/node_modules/@radix-ui/") ||
            normalizedId.includes("/node_modules/cmdk/") ||
            normalizedId.includes("/node_modules/class-variance-authority/") ||
            normalizedId.includes("/node_modules/clsx/") ||
            normalizedId.includes("/node_modules/tailwind-merge/")
          ) {
            return "vendor-ui";
          }
          if (
            normalizedId.includes("/node_modules/@sandbox-agent/react/") ||
            normalizedId.includes("/node_modules/sandbox-agent/") ||
            normalizedId.includes("/node_modules/acp-http-client/") ||
            normalizedId.includes("/node_modules/@tanstack/react-virtual/")
          ) {
            return "vendor-terminal";
          }
        },
      },
    },
  },
});
