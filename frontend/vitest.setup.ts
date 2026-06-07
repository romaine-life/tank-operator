// Setup for the jsdom (component / interaction) test project only. The pure
// `node` project does not load this file, so it never pays for DOM matchers or
// a DOM teardown it doesn't use.
import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// React Testing Library mounts into a shared document. Unmount and reset after
// every test so DOM state, focus, and event listeners never leak across tests.
afterEach(() => {
  cleanup();
});
