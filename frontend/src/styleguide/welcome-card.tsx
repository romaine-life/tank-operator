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
          auth error). Centered card with a primary CTA and an inline
          click-to-dismiss error pill rendered above when set.
        </p>
        <section style={sectionStyle}>
          <div style={{ background: "var(--bg-elevated)", border: "1px solid var(--border-strong)", borderRadius: "var(--radius-lg)", padding: 24, maxWidth: 480 }}>
            <div className="welcome-inner onboarding">
              <h2 className="welcome-title">Connect GitHub</h2>
              <p className="welcome-sub">
                tank-operator needs the App installed so it can run sessions
                against your repos. The next page is GitHub's; come back here
                after.
              </p>
              <a className="btn-primary onboarding-cta" href="#">Install</a>
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
