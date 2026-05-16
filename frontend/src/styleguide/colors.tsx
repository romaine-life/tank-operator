// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  rowStyle,
  SEMANTIC_SWATCHES,
  SURFACE_SWATCHES,
  Swatch,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

export function StyleguideColors() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>colors</h1>
        <p style={captionStyle}>
          Dark-only surfaces and small semantic accents. Hover states recess
          into darker fills; active/open states are lighter and easier to scan.
        </p>
        <section style={sectionStyle}>
          <div style={{ display: "grid", gap: 18 }}>
            <div style={rowStyle}>
              {SURFACE_SWATCHES.map(([label, token, value]) => (
                <Swatch key={token} label={label} token={token} value={value} />
              ))}
            </div>
            <div style={rowStyle}>
              {SEMANTIC_SWATCHES.map(([label, token, value]) => (
                <Swatch key={token} label={label} token={token} value={value} />
              ))}
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
