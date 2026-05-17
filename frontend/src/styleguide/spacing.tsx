// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  RADIUS_SAMPLES,
  rowStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

export function StyleguideSpacing() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>spacing and radii</h1>
        <p style={captionStyle}>
          Stable dimensions matter more than decorative depth. Use the radius
          ladder consistently so labels, icons, and hover states cannot shift
          nearby layout.
        </p>
        <section style={sectionStyle}>
          <div style={rowStyle}>
            {RADIUS_SAMPLES.map(([name, token, size]) => (
              <div key={token} style={{ display: "grid", gap: 8, width: 124 }}>
                <span
                  style={{
                    height: 54,
                    borderRadius: `var(${token})`,
                    background: "var(--bg-sidebar-control)",
                    border: "1px solid var(--row-rest-ring)",
                  }}
                />
                <span style={{ fontSize: "var(--text-xs)", color: "var(--text-muted)" }}>{name} · {size}</span>
                <code>{token}</code>
              </div>
            ))}
          </div>
        </section>
      </div>
    </div>
  );
}
