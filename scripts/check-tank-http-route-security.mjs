#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

const scenarios = [
  {
    name: "production",
    release: "tank-operator",
    namespace: "tank-operator",
    args: [],
    host: "tank.romaine.life",
  },
  {
    name: "validation-slot-warm",
    release: "tank-operator-slot-3-warm",
    namespace: "tank-operator-slot-3",
    args: [
      "--set",
      "renderMode=warm",
      "--set",
      "testEnv.slotName=tank-operator-slot-3",
      "--set",
      "testEnv.recordBase=tank.dev.romaine.life",
      "--set",
      "testEnv.wildcardListenerSetName=tank-operator-wildcard",
      "--set",
      "testEnv.wildcardListenerSetNamespace=tank-operator",
    ],
    host: "tank-operator-slot-3.tank.dev.romaine.life",
  },
];

function renderHelm({ release, namespace, args }) {
  const command = [
    "template",
    release,
    path.join(repoRoot, "k8s"),
    "--namespace",
    namespace,
    "--show-only",
    "templates/httproute.yaml",
    ...args,
  ];
  const result = spawnSync("helm", command, {
    cwd: repoRoot,
    encoding: "utf8",
  });
  if (result.status !== 0) {
    const detail = [result.stdout, result.stderr].filter(Boolean).join("\n");
    throw new Error(`helm ${command.join(" ")} failed:\n${detail}`);
  }
  return result.stdout;
}

function renderHelmAll({ release, namespace, args }) {
  const command = [
    "template",
    release,
    path.join(repoRoot, "k8s"),
    "--namespace",
    namespace,
    ...args,
  ];
  const result = spawnSync("helm", command, {
    cwd: repoRoot,
    encoding: "utf8",
  });
  if (result.status !== 0) {
    const detail = [result.stdout, result.stderr].filter(Boolean).join("\n");
    throw new Error(`helm ${command.join(" ")} failed:\n${detail}`);
  }
  return result.stdout;
}

function httpRouteDocs(rendered) {
  return rendered
    .split(/^---\s*$/m)
    .map((doc) => doc.trim())
    .filter((doc) => /\nkind:\s*HTTPRoute\s*(?:\n|$)/.test(`\n${doc}\n`));
}

function dnsEndpointDocs(rendered) {
  return rendered
    .split(/^---\s*$/m)
    .map((doc) => doc.trim())
    .filter((doc) => /\nkind:\s*DNSEndpoint\s*(?:\n|$)/.test(`\n${doc}\n`));
}

function metadataName(doc) {
  const metadata = doc.match(/^metadata:\n([\s\S]*?)(?=^[^\s][^:\n]*:|\s*$)/m)?.[1] ?? "";
  return metadata.match(/^  name:\s*(.+?)\s*$/m)?.[1] ?? "<unknown>";
}

function hostnames(doc) {
  const block = doc.match(/^  hostnames:\n((?:    - .+\n?)+)/m)?.[1] ?? "";
  return Array.from(block.matchAll(/^    - ["']?([^"'\n]+)["']?\s*$/gm), (match) => match[1]);
}

function hasHttpGatewayParent(doc) {
  return (
    /kind:\s*Gateway\b/.test(doc) &&
    /name:\s*main\b/.test(doc) &&
    /namespace:\s*envoy-gateway-system\b/.test(doc)
  );
}

function hasBackendRefs(doc) {
  return /^\s+-?\s*backendRefs:\s*$/m.test(doc);
}

function hasHttpsRedirect(doc) {
  return (
    hasHttpGatewayParent(doc) &&
    /sectionName:\s*http\b/.test(doc) &&
    /type:\s*RequestRedirect\b/.test(doc) &&
    /scheme:\s*https\b/.test(doc) &&
    /statusCode:\s*301\b/.test(doc) &&
    !hasBackendRefs(doc)
  );
}

function validateRendered(rendered, scenarioName, expectedHost) {
  const docs = httpRouteDocs(rendered);
  const failures = [];
  if (docs.length === 0) {
    failures.push(`${scenarioName}: rendered no HTTPRoute documents`);
  }

  for (const doc of docs) {
    if (hasHttpGatewayParent(doc) && hasBackendRefs(doc)) {
      failures.push(
        `${scenarioName}/${metadataName(doc)}: HTTP listener route must redirect, not forward to backendRefs`,
      );
    }
  }

  const redirects = docs.filter((doc) => hostnames(doc).includes(expectedHost) && hasHttpsRedirect(doc));
  if (redirects.length !== 1) {
    failures.push(
      `${scenarioName}: expected exactly one HTTPS redirect route for ${expectedHost}, found ${redirects.length}`,
    );
  }
  return failures;
}

function validateNoDefaultSlotSessionWildcard(scenario) {
  const rendered = renderHelmAll(scenario);
  const dnsDocs = dnsEndpointDocs(rendered);
  return dnsDocs
    .filter((doc) => metadataName(doc) === "tank-operator-sessions-wildcard")
    .map(
      () =>
        `${scenario.name}: validation slots must not publish *.${scenario.host} DNS by default because the shared certificate only covers *.tank.dev.romaine.life`,
    );
}

function runSelfTest() {
  const good = `
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: tank-operator
  namespace: tank-operator
spec:
  hostnames:
    - tank.romaine.life
  parentRefs:
    - group: gateway.networking.x-k8s.io
      kind: XListenerSet
      name: tank-operator
      namespace: tank-operator
  rules:
    - backendRefs:
        - name: tank-operator
          port: 80
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: tank-operator-http-redirect
  namespace: tank-operator
spec:
  hostnames:
    - tank.romaine.life
  parentRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: main
      namespace: envoy-gateway-system
      sectionName: http
  rules:
    - filters:
        - type: RequestRedirect
          requestRedirect:
            scheme: https
            statusCode: 301
`;
  const bad = good.replace("kind: XListenerSet", "kind: Gateway\n      name: main\n      namespace: envoy-gateway-system");
  const goodFailures = validateRendered(good, "self-good", "tank.romaine.life");
  const badFailures = validateRendered(bad, "self-bad", "tank.romaine.life");
  if (goodFailures.length > 0) {
    throw new Error(`self-test good fixture failed:\n${goodFailures.join("\n")}`);
  }
  if (!badFailures.some((failure) => failure.includes("must redirect"))) {
    throw new Error(`self-test bad fixture did not catch HTTP backend route:\n${badFailures.join("\n")}`);
  }
}

function main() {
  if (process.argv.includes("--self-test")) {
    runSelfTest();
    console.log("tank HTTP route security self-test passed");
    return;
  }

  const failures = [];
  for (const scenario of scenarios) {
    const rendered = renderHelm(scenario);
    failures.push(...validateRendered(rendered, scenario.name, scenario.host));
    if (scenario.name.startsWith("validation-slot-")) {
      failures.push(...validateNoDefaultSlotSessionWildcard(scenario));
    }
  }

  if (failures.length > 0) {
    console.error("Tank HTTP route security check failed:");
    for (const failure of failures) console.error(`- ${failure}`);
    process.exit(1);
  }
  console.log("Tank HTTP route security check passed");
}

main();
