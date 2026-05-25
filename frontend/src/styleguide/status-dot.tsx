// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  rowStyle,
  sectionStyle,
  STATUSES,
  styleguideShellStyle,
} from "./shared";

export function StyleguideStatusDot() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>status dot</h1>
        <p style={captionStyle}>
          Compact session activity state in the sidebar row. Color carries the
          durable lifecycle or agent state while the session name keeps the
          dominant row weight.
        </p>
        <section style={sectionStyle}>
          <div style={rowStyle}>
            {STATUSES.map(([status, label]) => (
              <div key={status} style={{ display: "flex", alignItems: "center", gap: 8 }}>
                <span className={`status-dot status-${status}`} aria-label={`status ${label}`} />
                <span style={{ fontSize: "var(--text-xs)", color: "var(--text-muted)" }}>{label}</span>
              </div>
            ))}
          </div>
        </section>
      </div>
    </div>
  );
}
