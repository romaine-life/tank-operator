import { readFileSync } from "node:fs";
import { expect, test } from "vitest";

import {
  DESIGN_SURFACE_GROUPS,
  DESIGN_SURFACE_TIERS,
  DESIGN_SURFACES,
  type DesignSurface,
} from "./design-surfaces";

function names(surfaces: readonly DesignSurface[]): string[] {
  return surfaces.map((surface) => surface.name);
}

test("design surface names are stable unique PascalCase identifiers", () => {
  const surfaceNames = names(DESIGN_SURFACES);
  expect(new Set(surfaceNames).size).toBe(surfaceNames.length);

  for (const surface of DESIGN_SURFACES) {
    expect(surface.name).toMatch(/^[A-Z][A-Za-z0-9]+$/);
    expect(surface.description.trim().length).toBeGreaterThan(24);
    expect(surface.source).toMatch(/^frontend\/src\/.+\.(ts|tsx)$/);
    expect(DESIGN_SURFACE_TIERS).toContain(surface.tier);
    expect(DESIGN_SURFACE_GROUPS).toContain(surface.group);
  }
});

test("design surface taxonomy covers the app route model", () => {
  expect(names(DESIGN_SURFACES)).toEqual(
    expect.arrayContaining([
      "CreateSessionScreen",
      "SessionWorkspaceScreen",
      "SessionTranscriptPane",
      "TurnActivityScreen",
      "StaticPreviewPane",
      "WorkspaceFilesPane",
      "SessionDataScreen",
      "BackgroundWorkScreen",
      "SettingsScreen",
      "AdminControlsScreen",
      "AdminAvatarManagerScreen",
      "SessionReportScreen",
      "ObservabilityScreen",
      "ClusterHealthScreen",
      "HelpScreen",
    ]),
  );
});

test("named surfaces styleguide route is registered in the catalog and router", () => {
  const mainSource = readFileSync(new URL("../main.tsx", import.meta.url), "utf8");
  const indexSource = readFileSync(new URL("./index.tsx", import.meta.url), "utf8");

  expect(mainSource).toContain('"/_styleguide/named-surfaces": () => <StyleguideNamedSurfaces />');
  expect(indexSource).toContain('{ slug: "named-surfaces", label: "named surfaces"');
});
