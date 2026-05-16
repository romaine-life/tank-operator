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

export function StyleguideButtons() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>buttons</h1>
        <p style={captionStyle}>
          Three voices: <code>btn-primary</code> for the dominant action on a
          screen (sign-in, install CTA), <code>btn-secondary</code> for
          recoveries (retry, cancel), <code>link-button</code> for inline
          text affordances. Disabled state is muted opacity, not a separate
          class.
        </p>
        <section style={sectionStyle}>
          <div style={rowStyle}>
            <button className="btn-primary">Sign in</button>
            <button className="btn-primary" disabled>Sign in</button>
            <button className="btn-secondary">retry</button>
            <button className="btn-secondary" disabled>retry</button>
            <button className="link-button">Sign out</button>
          </div>
        </section>
      </div>
    </div>
  );
}
