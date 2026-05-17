// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import { ArrowLeftIcon, FolderIcon, InfoIcon, SettingsIcon } from "lucide-react";
import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  sectionStyle,
  showcaseFrameStyle,
  styleguideShellStyle,
} from "./shared";

export function StyleguideRunHeaderTabs() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>run header tabs</h1>
        <p style={captionStyle}>
          Header tabs that open side-pane views inside a session. The label
          text must stay aligned with the icon at desktop width and remain
          readable in the narrow horizontal-scroll state.
        </p>
        <section style={sectionStyle}>
          <div style={{ display: "grid", gap: 14 }}>
            <div style={showcaseFrameStyle}>
              <section className="run-panel" style={{ minHeight: 116 }}>
                <header className="run-header">
                  <div className="run-header-title">
                    <button className="run-header-name-btn" type="button">
                      avatar-dinosaur-pool
                    </button>
                  </div>
                  <nav className="run-tabs" aria-label="Session actions">
                    <button
                      className="run-tab"
                      type="button"
                      aria-pressed={false}
                      data-testid="styleguide-run-tab-files"
                      data-design-component="RunHeaderTab"
                      data-design-state="rest"
                      data-design-source="frontend/src/App.tsx"
                    >
                      <FolderIcon className="run-tab-icon" strokeWidth={1.8} aria-hidden="true" />
                      <span>Files</span>
                    </button>
                    <button
                      className="run-tab run-tab-active"
                      type="button"
                      aria-pressed={true}
                      data-testid="styleguide-run-tab-settings-active"
                      data-design-component="RunHeaderTab"
                      data-design-state="active"
                      data-design-source="frontend/src/App.tsx"
                    >
                      <SettingsIcon className="run-tab-icon" aria-hidden="true" />
                      <span>Settings</span>
                    </button>
                    <button className="run-tab" type="button" aria-pressed={false}>
                      <InfoIcon className="run-tab-icon" aria-hidden="true" />
                      <span>Help</span>
                    </button>
                  </nav>
                </header>
              </section>
            </div>
            <div style={showcaseFrameStyle}>
              <section className="run-panel" style={{ minHeight: 116 }}>
                <header className="run-header">
                  <div className="run-header-title">
                    <button className="run-header-name-btn" type="button">
                      session-with-files-open
                    </button>
                  </div>
                  <nav className="run-tabs" aria-label="Session actions">
                    <button
                      className="run-tab run-tab-back"
                      type="button"
                      data-testid="styleguide-run-tab-back"
                      data-design-component="RunHeaderTab"
                      data-design-state="side-pane-back"
                      data-design-source="frontend/src/App.tsx"
                    >
                      <ArrowLeftIcon className="run-tab-icon" strokeWidth={2.2} aria-hidden="true" />
                      <span>Back</span>
                    </button>
                    <button
                      className="run-tab run-tab-active"
                      type="button"
                      aria-pressed={true}
                      data-testid="styleguide-run-tab-files-active"
                      data-design-component="RunHeaderTab"
                      data-design-state="side-pane-open"
                      data-design-source="frontend/src/App.tsx"
                    >
                      <FolderIcon className="run-tab-icon" strokeWidth={1.8} aria-hidden="true" />
                      <span>Files</span>
                    </button>
                    <button className="run-tab" type="button" aria-pressed={false}>
                      <SettingsIcon className="run-tab-icon" aria-hidden="true" />
                      <span>Settings</span>
                    </button>
                    <button className="run-tab" type="button" aria-pressed={false}>
                      <InfoIcon className="run-tab-icon" aria-hidden="true" />
                      <span>Help</span>
                    </button>
                  </nav>
                </header>
              </section>
            </div>
            <div style={{ ...showcaseFrameStyle, maxWidth: 390 }}>
              <section className="run-panel" style={{ minHeight: 142 }}>
                <header className="run-header">
                  <div className="run-header-title">
                    <button className="run-header-name-btn" type="button">
                      narrow-session
                    </button>
                  </div>
                  <nav className="run-tabs" aria-label="Session actions">
                    <button
                      className="run-tab run-tab-back"
                      type="button"
                      data-testid="styleguide-run-tab-back-narrow"
                      data-design-component="RunHeaderTab"
                      data-design-state="narrow-side-pane-back"
                      data-design-source="frontend/src/App.tsx"
                    >
                      <ArrowLeftIcon className="run-tab-icon" strokeWidth={2.2} aria-hidden="true" />
                      <span>Back</span>
                    </button>
                    <button
                      className="run-tab run-tab-active"
                      type="button"
                      aria-pressed={true}
                      data-testid="styleguide-run-tab-files-narrow-active"
                      data-design-component="RunHeaderTab"
                      data-design-state="narrow-side-pane-open"
                      data-design-source="frontend/src/App.tsx"
                    >
                      <FolderIcon className="run-tab-icon" strokeWidth={1.8} aria-hidden="true" />
                      <span>Files</span>
                    </button>
                    <button className="run-tab" type="button" aria-pressed={false}>
                      <SettingsIcon className="run-tab-icon" aria-hidden="true" />
                      <span>Settings</span>
                    </button>
                    <button className="run-tab" type="button" aria-pressed={false}>
                      <InfoIcon className="run-tab-icon" aria-hidden="true" />
                      <span>Help</span>
                    </button>
                  </nav>
                </header>
              </section>
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
