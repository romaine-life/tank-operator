import {
  DESIGN_SURFACE_GROUPS,
  DESIGN_SURFACE_TIERS,
  DESIGN_SURFACES,
  designSurfacesByGroup,
  designSurfacesByTier,
  type DesignSurface,
  type DesignSurfaceGroup,
  type DesignSurfaceTier,
} from "./design-surfaces";
import {
  BackLink,
  captionStyle,
  headStyle,
  pageTitleStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

const GROUP_LABELS: Record<DesignSurfaceGroup, string> = {
  workspace: "workspace",
  create: "create",
  conversation: "conversation",
  activity: "activity",
  data: "data",
  admin: "admin",
  debug: "debug",
  foundation: "foundation",
};

const TIER_LABELS: Record<DesignSurfaceTier, string> = {
  screen: "screen",
  pane: "pane",
  region: "region",
  component: "component",
  primitive: "primitive",
};

const tierTone: Record<DesignSurfaceTier, { color: string; background: string; border: string }> = {
  screen: {
    color: "var(--text-primary)",
    background: "rgba(79, 140, 247, 0.14)",
    border: "rgba(79, 140, 247, 0.28)",
  },
  pane: {
    color: "var(--cyan-bright)",
    background: "var(--cyan-bg)",
    border: "var(--cyan-border)",
  },
  region: {
    color: "var(--skill-test-fg)",
    background: "var(--skill-test-bg)",
    border: "var(--skill-test-border)",
  },
  component: {
    color: "var(--mode-claude-gui-fg)",
    background: "var(--mode-claude-gui-bg)",
    border: "rgba(229, 169, 132, 0.22)",
  },
  primitive: {
    color: "var(--text-secondary)",
    background: "var(--bg-sidebar-control)",
    border: "var(--row-rest-ring)",
  },
};

function SurfaceBadge({ tier }: { tier: DesignSurfaceTier }) {
  const tone = tierTone[tier];
  return (
    <span
      style={{
        display: "inline-flex",
        alignItems: "center",
        minHeight: 22,
        padding: "0 8px",
        border: `1px solid ${tone.border}`,
        borderRadius: "var(--radius-pill)",
        color: tone.color,
        background: tone.background,
        fontSize: "var(--text-xs)",
        whiteSpace: "nowrap",
      }}
    >
      {TIER_LABELS[tier]}
    </span>
  );
}

function SurfaceRow({ surface }: { surface: DesignSurface }) {
  return (
    <li
      style={{
        display: "grid",
        gridTemplateColumns: "repeat(auto-fit, minmax(min(100%, 240px), 1fr))",
        gap: 14,
        alignItems: "start",
        padding: "14px 0",
        borderTop: "1px solid var(--border-subtle)",
      }}
    >
      <div style={{ display: "grid", gap: 6 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
          <code style={{ color: "var(--text-primary)", fontSize: "var(--text-sm)" }}>
            {surface.name}
          </code>
          <SurfaceBadge tier={surface.tier} />
        </div>
        {surface.aliases && surface.aliases.length > 0 && (
          <span style={{ fontSize: "var(--text-xs)", color: "var(--text-faint)" }}>
            formerly: {surface.aliases.join(", ")}
          </span>
        )}
      </div>
      <p style={{ ...captionStyle, margin: 0, maxWidth: "none" }}>{surface.description}</p>
      <div style={{ display: "grid", gap: 5, minWidth: 0 }}>
        {surface.route && (
          <code style={{ fontSize: "var(--text-xs)", overflowWrap: "anywhere" }}>
            {surface.route}
          </code>
        )}
        <span style={{ fontSize: "var(--text-xs)", color: "var(--text-muted)", overflowWrap: "anywhere" }}>
          {surface.source}
        </span>
      </div>
    </li>
  );
}

function SurfaceGroupSection({ group }: { group: DesignSurfaceGroup }) {
  const surfaces = designSurfacesByGroup(group);
  if (surfaces.length === 0) return null;
  return (
    <section style={sectionStyle}>
      <h2 style={headStyle}>{GROUP_LABELS[group]}</h2>
      <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
        {surfaces.map((surface) => (
          <SurfaceRow key={surface.name} surface={surface} />
        ))}
      </ul>
    </section>
  );
}

export function StyleguideNamedSurfaces() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 1080 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>named surfaces</h1>
        <p style={captionStyle}>
          Stable product names for Tank screens, panes, regions, components,
          and primitives. Use these names in design notes, PR descriptions,
          tests, and <code>data-design-component</code> values.
        </p>

        <section style={sectionStyle}>
          <h2 style={headStyle}>naming rules</h2>
          <div
            style={{
              display: "grid",
              gridTemplateColumns: "repeat(auto-fit, minmax(190px, 1fr))",
              gap: 10,
            }}
          >
            {DESIGN_SURFACE_TIERS.map((tier) => (
              <div
                key={tier}
                style={{
                  border: "1px solid var(--border-soft)",
                  borderRadius: "var(--radius-md)",
                  background: "var(--bg-elevated)",
                  padding: 12,
                  display: "grid",
                  gap: 8,
                }}
              >
                <SurfaceBadge tier={tier} />
                <span style={{ fontSize: "var(--text-xs)", color: "var(--text-faint)" }}>
                  {designSurfacesByTier(tier).length} named {TIER_LABELS[tier]}
                  {designSurfacesByTier(tier).length === 1 ? "" : "s"}
                </span>
              </div>
            ))}
          </div>
          <p style={{ ...captionStyle, marginTop: 14 }}>
            Screens are routable destinations. Panes are routable sub-surfaces
            inside a session. Regions persist around panes. Components compose
            product behavior. Primitives are reusable visual atoms.
          </p>
        </section>

        {DESIGN_SURFACE_GROUPS.map((group) => (
          <SurfaceGroupSection key={group} group={group} />
        ))}

        <p style={{ ...captionStyle, marginTop: 24 }}>
          Total named surfaces: {DESIGN_SURFACES.length}.
        </p>
      </div>
    </div>
  );
}
