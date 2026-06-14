import { test, expect } from "vitest";

import {
  breadcrumbCompactLabel,
  breadcrumbTrail,
  breadcrumbUpHref,
  type BreadcrumbLocation,
} from "./breadcrumb";

const HREF = "https://tank.example.test/sessions/s-1";
const loc = (over: Partial<BreadcrumbLocation>): BreadcrumbLocation => ({
  tab: "chat",
  turnNumber: null,
  pageNumber: null,
  staticPath: null,
  turnUnavailable: false,
  ...over,
});

test("chat surface is a single current 'main transcript' crumb", () => {
  // Turns is the primary session view, so the main transcript is its own
  // explicit /transcript route, not the bare session root.
  expect(
    breadcrumbTrail("s-1", loc({ tab: "chat" }), HREF).map((c) => [
      c.label,
      c.href,
      c.current,
    ]),
  ).toEqual([
    ["main transcript", "https://tank.example.test/sessions/s-1/transcript", true],
  ]);
});

test("a numbered turn with a page yields turns / N / pages / P, leaf current", () => {
  expect(
    breadcrumbTrail(
      "s-1",
      loc({ tab: "turns", turnNumber: 3, pageNumber: 2 }),
      HREF,
    ).map((c) => [c.label, c.href, c.current]),
  ).toEqual([
    ["turns", "https://tank.example.test/sessions/s-1", false],
    ["3", "https://tank.example.test/sessions/s-1/turns/3", false],
    ["pages", null, false],
    ["2", "https://tank.example.test/sessions/s-1/turns/3/pages/2", true],
  ]);
});

test("a turn with no page makes the turn crumb the current leaf", () => {
  expect(
    breadcrumbTrail("s-1", loc({ tab: "turns", turnNumber: 5 }), HREF).map(
      (c) => [c.label, c.current],
    ),
  ).toEqual([
    ["turns", false],
    ["5", true],
  ]);
});

test("the in-progress turn with no durable number renders as 'current'", () => {
  const trail = breadcrumbTrail("s-1", loc({ tab: "turns" }), HREF);
  expect(trail[1].label).toBe("current");
  expect(trail[1].current).toBe(true);
});

test("an unavailable turn target is an explicit non-link leaf", () => {
  expect(
    breadcrumbTrail(
      "s-1",
      loc({ tab: "turns", turnUnavailable: true }),
      HREF,
    ).map((c) => [c.label, c.href, c.current]),
  ).toEqual([
    ["turns", "https://tank.example.test/sessions/s-1", false],
    ["unavailable", null, true],
  ]);
});

test("static surface is files / <path>, path current", () => {
  expect(
    breadcrumbTrail(
      "s-1",
      loc({ tab: "static", staticPath: "out/report.html" }),
      HREF,
    ).map((c) => [c.label, c.href, c.current]),
  ).toEqual([
    ["files", null, false],
    [
      "out/report.html",
      "https://tank.example.test/sessions/s-1/static/out/report.html",
      true,
    ],
  ]);
});

test("session-data root and app-level tabs have no trailing trail", () => {
  expect(breadcrumbTrail("s-1", loc({ tab: "session-data" }), HREF)).toEqual([]);
  expect(breadcrumbTrail("s-1", loc({ tab: "settings" }), HREF)).toEqual([]);
  expect(breadcrumbTrail("s-1", loc({ tab: "help" }), HREF)).toEqual([]);
});

test("file-browser, PR, and background surfaces render a single current crumb", () => {
  expect(
    breadcrumbTrail("s-1", loc({ tab: "files" }), HREF).map((c) => [
      c.label,
      c.href,
      c.current,
    ]),
  ).toEqual([["files", "https://tank.example.test/sessions/s-1/files", true]]);
  expect(
    breadcrumbTrail("s-1", loc({ tab: "background" }), HREF).map((c) => [
      c.label,
      c.href,
      c.current,
    ]),
  ).toEqual([
    ["background", "https://tank.example.test/sessions/s-1/background", true],
  ]);
  expect(
    breadcrumbTrail("s-1", loc({ tab: "pull-requests" }), HREF).map((c) => [
      c.label,
      c.href,
      c.current,
    ]),
  ).toEqual([
    ["pull-requests", "https://tank.example.test/sessions/s-1/pull-requests", true],
  ]);
});

test("compact mobile label is the joined trail; null at base/root", () => {
  expect(breadcrumbCompactLabel(loc({ tab: "chat" }))).toBe(null);
  expect(breadcrumbCompactLabel(loc({ tab: "session-data" }))).toBe(null);
  expect(
    breadcrumbCompactLabel(loc({ tab: "turns", turnNumber: 3, pageNumber: 2 })),
  ).toBe("turns / 3 / pages / 2");
  expect(
    breadcrumbCompactLabel(loc({ tab: "static", staticPath: "a/b.html" })),
  ).toBe("files / a/b.html");
  expect(breadcrumbCompactLabel(loc({ tab: "pull-requests" }))).toBe("pull-requests");
});

test("mobile up-href climbs one navigable ancestor", () => {
  expect(
    breadcrumbUpHref(
      "s-1",
      loc({ tab: "turns", turnNumber: 3, pageNumber: 2 }),
      HREF,
    ),
  ).toBe("https://tank.example.test/sessions/s-1/turns/3");
  expect(
    breadcrumbUpHref("s-1", loc({ tab: "turns", turnNumber: 3 }), HREF),
  ).toBe("https://tank.example.test/sessions/s-1");
  expect(breadcrumbUpHref("s-1", loc({ tab: "chat" }), HREF)).toBe(
    "https://tank.example.test/sessions/s-1/session-data",
  );
  expect(breadcrumbUpHref("s-1", loc({ tab: "session-data" }), HREF)).toBe(null);
});
