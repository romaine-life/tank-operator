// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

export function StyleguideBootState() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>boot state</h1>
        <p style={captionStyle}>
          Full-screen states for app lifecycle (loading, sign-in needed, auth
          error). Renders in <code>div.boot-state</code> — single centered
          element + minimal chrome.
        </p>
        <section style={sectionStyle}>
          <div className="boot-state" style={{ minHeight: 80, padding: 16, border: "1px dashed var(--border-strong)", borderRadius: "var(--radius-md)" }}>
            <span className="boot-text">loading…</span>
          </div>
        </section>
      </div>
    </div>
  );
}
