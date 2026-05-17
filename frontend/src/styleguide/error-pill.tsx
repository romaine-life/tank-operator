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
  styleguideShellStyle,
} from "./shared";

export function StyleguideErrorPill() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>error pill</h1>
        <p style={captionStyle}>
          Inline error surface, click-to-dismiss. Used for transient failures
          (lost socket, save failed, install error) above the active card or
          in the sidebar.
        </p>
        <section style={sectionStyle}>
          <div style={rowStyle}>
            <pre className="error" style={{ margin: 0 }}>save failed: 500</pre>
          </div>
        </section>
      </div>
    </div>
  );
}
