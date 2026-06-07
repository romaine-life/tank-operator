import { defineConfig } from "vitest/config";
import path from "node:path";
import react from "@vitejs/plugin-react";

// The frontend has two test layers, split by file extension so the boundary is
// self-documenting and the wrong environment can't leak in:
//
//   *.test.ts   — pure logic. Runs in the `node` environment with no DOM. This
//                 is the existing suite (appRoutes, navigationMode, breadcrumb,
//                 conversation*, the migrationPolicy pin guard, etc.).
//   *.test.tsx  — component / interaction. Runs in `jsdom` with
//                 @testing-library/react + user-event. Renders real React and
//                 asserts on accessible output and user-driven behavior.
//
// Vitest reuses the project's Vite resolution, so the `@/` alias and the React
// plugin match the app build — there is no second transform pipeline to keep in
// sync. See docs/testing.md → "Frontend test layers" for the conventions and
// the deliberate jsdom-vs-browser-mode decision.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "src"),
    },
  },
  test: {
    globals: true,
    projects: [
      {
        extends: true,
        test: {
          name: "unit",
          environment: "node",
          include: ["src/**/*.test.ts"],
        },
      },
      {
        extends: true,
        test: {
          name: "dom",
          environment: "jsdom",
          include: ["src/**/*.test.tsx"],
          setupFiles: ["./vitest.setup.ts"],
        },
      },
    ],
  },
});
