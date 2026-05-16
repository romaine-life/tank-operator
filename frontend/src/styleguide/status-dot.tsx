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
          Replaces the old text pill ("Active" / "Pending" / "Failed") in
          the session row. Color carries the status; shape stays neutral so
          the row's dominant visual belongs to the session name.
        </p>
        <section style={sectionStyle}>
          <div style={rowStyle}>
            {STATUSES.map((s) => (
              <div key={s} style={{ display: "flex", alignItems: "center", gap: 8 }}>
                <span className={`status-dot status-${s}`} aria-label={`status ${s}`} />
                <span style={{ fontSize: "var(--text-xs)", color: "var(--text-muted)" }}>{s}</span>
              </div>
            ))}
          </div>
        </section>
      </div>
    </div>
  );
}
