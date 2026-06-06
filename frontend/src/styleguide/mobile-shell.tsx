// Compact / mobile shell scene. Shows the phone-posture top bar (home + active
// session) and the desktop-only boundary card. The full off-canvas drawer is a
// live radix Dialog (components/ui/sheet.tsx) wired in App.tsx; here we catalog
// the static chrome it exposes. See docs/design-system.md -> "Compact / mobile
// posture" and the app-chrome "Mobile Session Triage" capability.

import { MobileTopBar } from "../MobileTopBar";
import { AgentAvatarIcon, requireSessionAvatar } from "../sessionAvatars";
import {
  BackLink,
  captionStyle,
  headStyle,
  pageTitleStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

const frameStyle: React.CSSProperties = {
  maxWidth: 390,
  border: "1px solid var(--border-soft)",
  borderRadius: "var(--radius-md)",
  overflow: "hidden",
  background: "var(--bg-app)",
};

export function StyleguideMobileShell() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>compact shell</h1>
        <p style={captionStyle}>
          Phone posture (&le; 768px): the 260px sidebar collapses into an
          off-canvas drawer and this top bar carries the drawer trigger plus the
          current session context. Triage only — list, read, reply, answer,
          stop. See docs/design-system.md → “Compact / mobile posture”.
        </p>

        <section style={sectionStyle}>
          <h2 style={headStyle}>top bar — home</h2>
          <div style={frameStyle}>
            <MobileTopBar isHome onOpenNav={() => {}} />
          </div>
        </section>

        <section style={sectionStyle}>
          <h2 style={headStyle}>top bar — active session</h2>
          <div style={frameStyle}>
            <MobileTopBar
              isHome={false}
              sessionName="migration-plan"
              avatar={
                <AgentAvatarIcon
                  avatar={requireSessionAvatar("jp1-raptor")}
                  className="mobile-topbar-avatar"
                />
              }
              statusDotClass="status-dot status-agent-working"
              statusLabel="Agent working"
              onOpenNav={() => {}}
            />
          </div>
        </section>

        <section style={sectionStyle}>
          <h2 style={headStyle}>desktop-only boundary</h2>
          <p style={captionStyle}>
            Surfaces that can’t work on a phone (terminal attach, the avatar
            editor) render this honest card instead of a broken view.
          </p>
          <div style={{ ...frameStyle, height: 200 }}>
            <div className="desktop-only" role="note">
              <div className="desktop-only-inner">
                <p className="desktop-only-title">
                  terminal sessions is desktop-only
                </p>
                <p className="desktop-only-body">
                  terminal attach needs a keyboard and a wider screen — open this
                  session on desktop or tablet.
                </p>
              </div>
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
