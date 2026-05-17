// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  sectionStyle,
  showcaseFrameStyle,
  styleguideShellStyle,
} from "./shared";

export function StyleguidePortfolioOnboarding() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>portfolio scene: onboarding</h1>
        <p style={captionStyle}>
          First-run wall. This stays sparse: one task, one primary CTA,
          diagnostic supporting copy.
        </p>
        <section style={sectionStyle}>
          <div
            style={{
              ...showcaseFrameStyle,
              minHeight: 300,
              display: "grid",
              placeItems: "center",
              padding: 24,
            }}
          >
            <div className="welcome-inner onboarding" style={{ maxWidth: 460 }}>
              <h2 className="welcome-title">Connect GitHub</h2>
              <p className="welcome-sub">
                tank-operator needs the App installed so sessions can read and
                write repos through mcp-github.
              </p>
              <a className="btn-primary onboarding-cta" href="#">Install GitHub App</a>
              <p className="onboarding-meta">
                signed in as <code>you@example.com</code> ·{" "}
                <button className="link-button" type="button">Sign out</button>
              </p>
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
