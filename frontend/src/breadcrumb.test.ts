import assert from "node:assert/strict";
import test from "node:test";

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
  assert.deepEqual(
    breadcrumbTrail("s-1", loc({ tab: "chat" }), HREF).map((c) => [
      c.label,
      c.href,
      c.current,
    ]),
    [["main transcript", "https://tank.example.test/sessions/s-1", true]],
  );
});

test("a numbered turn with a page yields turns / N / pages / P, leaf current", () => {
  assert.deepEqual(
    breadcrumbTrail(
      "s-1",
      loc({ tab: "turns", turnNumber: 3, pageNumber: 2 }),
      HREF,
    ).map((c) => [c.label, c.href, c.current]),
    [
      ["turns", "https://tank.example.test/sessions/s-1/turns", false],
      ["3", "https://tank.example.test/sessions/s-1/turns/3", false],
      ["pages", null, false],
      ["2", "https://tank.example.test/sessions/s-1/turns/3/pages/2", true],
    ],
  );
});

test("a turn with no page makes the turn crumb the current leaf", () => {
  assert.deepEqual(
    breadcrumbTrail("s-1", loc({ tab: "turns", turnNumber: 5 }), HREF).map(
      (c) => [c.label, c.current],
    ),
    [
      ["turns", false],
      ["5", true],
    ],
  );
});

test("the in-progress turn with no durable number renders as 'current'", () => {
  const trail = breadcrumbTrail("s-1", loc({ tab: "turns" }), HREF);
  assert.equal(trail[1].label, "current");
  assert.equal(trail[1].current, true);
});

test("an unavailable turn target is an explicit non-link leaf", () => {
  assert.deepEqual(
    breadcrumbTrail(
      "s-1",
      loc({ tab: "turns", turnUnavailable: true }),
      HREF,
    ).map((c) => [c.label, c.href, c.current]),
    [
      ["turns", "https://tank.example.test/sessions/s-1/turns", false],
      ["unavailable", null, true],
    ],
  );
});

test("static surface is files / <path>, path current", () => {
  assert.deepEqual(
    breadcrumbTrail(
      "s-1",
      loc({ tab: "static", staticPath: "out/report.html" }),
      HREF,
    ).map((c) => [c.label, c.href, c.current]),
    [
      ["files", null, false],
      [
        "out/report.html",
        "https://tank.example.test/sessions/s-1/static/out/report.html",
        true,
      ],
    ],
  );
});

test("session-data root and app-level tabs have no trailing trail", () => {
  assert.deepEqual(
    breadcrumbTrail("s-1", loc({ tab: "session-data" }), HREF),
    [],
  );
  assert.deepEqual(breadcrumbTrail("s-1", loc({ tab: "settings" }), HREF), []);
  assert.deepEqual(breadcrumbTrail("s-1", loc({ tab: "help" }), HREF), []);
});

test("file-browser and background surfaces render a single current crumb", () => {
  assert.deepEqual(
    breadcrumbTrail("s-1", loc({ tab: "files" }), HREF).map((c) => [
      c.label,
      c.href,
      c.current,
    ]),
    [["files", "https://tank.example.test/sessions/s-1/files", true]],
  );
  assert.deepEqual(
    breadcrumbTrail("s-1", loc({ tab: "background" }), HREF).map((c) => [
      c.label,
      c.href,
      c.current,
    ]),
    [["background", "https://tank.example.test/sessions/s-1/background", true]],
  );
});

test("compact mobile label is the joined trail; null at base/root", () => {
  assert.equal(breadcrumbCompactLabel(loc({ tab: "chat" })), null);
  assert.equal(breadcrumbCompactLabel(loc({ tab: "session-data" })), null);
  assert.equal(
    breadcrumbCompactLabel(loc({ tab: "turns", turnNumber: 3, pageNumber: 2 })),
    "turns / 3 / pages / 2",
  );
  assert.equal(
    breadcrumbCompactLabel(loc({ tab: "static", staticPath: "a/b.html" })),
    "files / a/b.html",
  );
});

test("mobile up-href climbs one navigable ancestor", () => {
  assert.equal(
    breadcrumbUpHref(
      "s-1",
      loc({ tab: "turns", turnNumber: 3, pageNumber: 2 }),
      HREF,
    ),
    "https://tank.example.test/sessions/s-1/turns/3",
  );
  assert.equal(
    breadcrumbUpHref("s-1", loc({ tab: "turns", turnNumber: 3 }), HREF),
    "https://tank.example.test/sessions/s-1/turns",
  );
  assert.equal(
    breadcrumbUpHref("s-1", loc({ tab: "chat" }), HREF),
    "https://tank.example.test/sessions/s-1/session-data",
  );
  assert.equal(
    breadcrumbUpHref("s-1", loc({ tab: "session-data" }), HREF),
    null,
  );
});
