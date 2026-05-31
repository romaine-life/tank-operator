// Curated reference links surfaced in the admin Settings -> Admin tab.
//
// Per the App Chrome contract, external documentation URLs are *content
// references*, not product state: "External help/documentation URLs are content
// references; they are not product state." So the canonical source of truth for
// this list is this typed module, not a backend endpoint or a durable store.
// Hardcoding here (rather than serving from /api/admin/links) is deliberate --
// adding a DB table + endpoint + cache for six static doc URLs would be
// compatibility/ceremony with no durability or cross-device requirement to
// justify it. If these ever need to be user-editable at runtime, that is the
// point to promote them to server-owned storage.
//
// Links target the nelsong6/tank-operator default branch so they always track
// the live docs. Update this list when the session-config docs move.

export interface AdminReferenceLink {
  /** Stable id, used for React keys, tests, and click telemetry. */
  id: string;
  /** Row label. */
  label: string;
  /** One-line description of what the file is. */
  description: string;
  /** Absolute https URL. Opened in a new tab. */
  href: string;
}

const BLOB = "https://github.com/nelsong6/tank-operator/blob/main";
const TREE = "https://github.com/nelsong6/tank-operator/tree/main";

export const ADMIN_REFERENCE_LINKS: AdminReferenceLink[] = [
  {
    id: "default-claude",
    label: "Default session instructions",
    description:
      "The CLAUDE.md / AGENTS.md primer seeded into every session pod's workspace.",
    href: `${BLOB}/k8s/session-config/default-claude.md`,
  },
  {
    id: "quality-timeframes",
    label: "Quality timeframes",
    description:
      "The long-term, heavy-solution quality bar to read before substantial work.",
    href: `${BLOB}/k8s/session-config/docs/quality-timeframes.md`,
  },
  {
    id: "migration-policy",
    label: "Migration policy",
    description: "The migration and cleanup checklist sessions must follow.",
    href: `${BLOB}/k8s/session-config/docs/migration-policy.md`,
  },
  {
    id: "product-inspirations",
    label: "Product inspirations",
    description: "Product and architecture decision references.",
    href: `${BLOB}/k8s/session-config/docs/product-inspirations.md`,
  },
  {
    id: "session-config",
    label: "Session config bundle",
    description:
      "The whole k8s/session-config tree: MCP config, bootstrap, primer, skills.",
    href: `${TREE}/k8s/session-config`,
  },
  {
    id: "developer-guide",
    label: "Developer guide (CLAUDE.md)",
    description:
      "Repo-root guide for working on the tank-operator codebase itself.",
    href: `${BLOB}/CLAUDE.md`,
  },
];
