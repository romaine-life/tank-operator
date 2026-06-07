// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import {
  ActivityIcon,
  ArrowLeftIcon,
  EllipsisVerticalIcon,
  FolderIcon,
  InfoIcon,
  SettingsIcon,
} from "lucide-react";
import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  sectionStyle,
  showcaseFrameStyle,
  styleguideShellStyle,
} from "./shared";

// The single overflow control for secondary top-right session actions.
// Mirrors RunHeaderOverflowMenu's trigger in App.tsx.
function MoreTab({
  active = false,
  attention = false,
}: {
  active?: boolean;
  attention?: boolean;
}) {
  return (
    <button
      className={`run-tab run-tab-more${active ? " run-tab-active" : ""}`}
      type="button"
      aria-label="More session actions"
      aria-pressed={active}
      title="More"
      data-design-component="RunHeaderOverflowMenu"
      data-design-state={active ? "more-active" : attention ? "more-attention" : "more-rest"}
      data-design-source="frontend/src/App.tsx"
    >
      <EllipsisVerticalIcon className="run-tab-icon" aria-hidden="true" />
      {attention && (
        <span className="run-tab-alert is-warning" aria-hidden="true" />
      )}
    </button>
  );
}

// Static specimen of one open overflow-menu row. The real rows are Radix
// DropdownMenuItems; here a plain button carries the same classes so the
// menu's density and states stay reviewable.
function MoreMenuItem({
  icon,
  label,
  active = false,
  disabled = false,
  count,
  countActive = false,
  attention = false,
}: {
  icon: React.ReactNode;
  label: string;
  active?: boolean;
  disabled?: boolean;
  count?: number;
  countActive?: boolean;
  attention?: boolean;
}) {
  return (
    <button
      type="button"
      className={`run-tab-more-item${active ? " is-active" : ""}`}
      style={{ display: "flex", alignItems: "center", width: "100%" }}
      disabled={disabled}
      aria-disabled={disabled || undefined}
    >
      {icon}
      <span>{label}</span>
      {count !== undefined && (
        <span
          className="run-shell-tasks-count run-tab-more-item-count"
          data-active={countActive ? "true" : undefined}
        >
          {count}
        </span>
      )}
      {attention && <span className="run-tab-alert is-warning" aria-hidden="true" />}
    </button>
  );
}

// The open menu panel — secondary top-right actions live here.
function MoreMenuPanel() {
  return (
    <div className="run-tab-more-menu" role="menu" aria-label="Session actions">
      <MoreMenuItem
        icon={<ActivityIcon className="run-tab-more-item-icon" aria-hidden="true" />}
        label="Background"
        count={2}
        countActive
      />
      <MoreMenuItem
        icon={<FolderIcon className="run-tab-more-item-icon" strokeWidth={1.8} aria-hidden="true" />}
        label="Files"
        active
      />
      <div className="run-tab-more-separator" style={{ height: 1, margin: "0.3rem -0.35rem" }} />
      <MoreMenuItem
        icon={<SettingsIcon className="run-tab-more-item-icon" aria-hidden="true" />}
        label="Settings"
        attention
      />
      <MoreMenuItem
        icon={<InfoIcon className="run-tab-more-item-icon" aria-hidden="true" />}
        label="Help"
      />
    </div>
  );
}

export function StyleguideRunHeaderTabs() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>run header tabs</h1>
        <p style={captionStyle}>
          The header keeps Turns as the primary session surface and collapses
          secondary top-right actions — Background / Files / Session data plus
          Settings / Help — into a single vertical overflow control (⋮). The
          header stays a clean title-plus-menu strip at any width; live counts
          and an attention dot ride the menu so ambient signal survives when it
          is closed.
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
                    <MoreTab attention />
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
                    <MoreTab active />
                  </nav>
                </header>
              </section>
            </div>
            <div style={showcaseFrameStyle}>
              <p style={captionStyle}>Open overflow menu</p>
              <div style={{ display: "flex", justifyContent: "flex-end" }}>
                <MoreMenuPanel />
              </div>
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
