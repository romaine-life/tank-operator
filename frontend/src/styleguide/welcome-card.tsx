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

export function StyleguideWelcomeCard() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>welcome card</h1>
        <p style={captionStyle}>
          The shape used for boot states (sign-in, GitHub install onboarding,
          auth error). Centered content with a primary CTA and an inline
          click-to-dismiss onboarding error rendered above when set.
        </p>
        <section style={sectionStyle}>
          <div style={{ background: "var(--bg-elevated)", border: "1px solid var(--border-strong)", borderRadius: "var(--radius-lg)", padding: 24, maxWidth: 480 }}>
            <div className="welcome-inner onboarding">
              <h2 className="welcome-title">Connect GitHub</h2>
              <p className="welcome-sub">
                tank-operator needs the <code>tank-operator</code> GitHub App
                installed on your account so your sessions can read and write
                your repos via mcp-github.
              </p>
              <pre className="error onboarding-error" title="dismiss">
                GitHub install failed. Try again or choose another account.
              </pre>
              <a className="btn-primary onboarding-cta" href="#">Install GitHub App</a>
              <p className="onboarding-meta">
                Signed in as <strong>you@example.com</strong>.{" "}
                <button className="link-button" type="button">sign out</button>
              </p>
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
