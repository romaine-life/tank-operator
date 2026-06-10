import {
  BugIcon,
  ClipboardListIcon,
  FlaskConicalIcon,
  GavelIcon,
  MessageSquareTextIcon,
  MonitorIcon,
  SearchCheckIcon,
  TerminalIcon,
} from "lucide-react";
import { ProviderIcon } from "../providerIcons";
import {
  BackLink,
  captionStyle,
  IconWrench,
  pageTitleStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

export function StyleguideNewSessionRow() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 980 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>session launcher</h1>
        <p style={captionStyle}>
          Current pre-session home setup: provider, interaction, initial
          message mode, model/effort, setup shortcuts, and workspace repos.
          The old compact sidebar new-session row is no longer an app surface.
        </p>
        <section style={sectionStyle}>
          <div className="run-main-home" style={{ padding: 0 }}>
            <div className="home-inner">
              <section className="home-hero" aria-labelledby="styleguide-home-title">
                <div>
                  <h2 id="styleguide-home-title" className="home-title">
                    What do you want to build?
                  </h2>
                  <p className="home-sub">
                    Type below to start a session with the selected runtime.
                  </p>
                </div>
                <span className="home-count">3 sessions</span>
              </section>

              <div className="home-grid">
                <section className="home-panel" aria-labelledby="styleguide-home-start-title">
                  <div className="home-panel-head">
                    <h3 id="styleguide-home-start-title">Configuration</h3>
                    <span className="home-panel-meta">Claude GUI</span>
                  </div>
                  <div className="home-choice-grid" role="group" aria-label="provider">
                    <button className="home-choice home-provider-choice is-low is-selected" type="button" aria-pressed="true" title="Claude GUI">
                      <span className="home-provider-choice-main">
                        <ProviderIcon provider="anthropic" className="home-choice-icon" />
                        <span>Claude</span>
                      </span>
                      <span className="home-provider-choice-usage">
                        <span>5h 18% left</span>
                        <span>Week 64% left</span>
                      </span>
                    </button>
                    <button className="home-choice home-provider-choice is-unknown" type="button" aria-pressed="false" title="Codex GUI">
                      <span className="home-provider-choice-main">
                        <ProviderIcon provider="codex" className="home-choice-icon" />
                        <span>Codex</span>
                      </span>
                      <span className="home-provider-choice-usage">
                        <span>5h unknown</span>
                        <span>Week unknown</span>
                      </span>
                    </button>
                  </div>
                  <div className="home-provider-capacity-panel is-low" aria-label="Claude usage remaining">
                    <div className="home-provider-capacity-head">
                      <span>Capacity</span>
                      <span>Last captured 14:20:00</span>
                    </div>
                    <div className="home-provider-capacity-rows">
                      <div className="home-provider-capacity-row is-low">
                        <span className="home-provider-capacity-label">5-hour window</span>
                        <span className="home-provider-capacity-meter" aria-hidden="true">
                          <span style={{ width: "18%" }} />
                        </span>
                        <span className="home-provider-capacity-value">18% left · resets Jun 5, 2026, 5:00 PM UTC</span>
                      </div>
                      <div className="home-provider-capacity-row is-ok">
                        <span className="home-provider-capacity-label">Weekly</span>
                        <span className="home-provider-capacity-meter" aria-hidden="true">
                          <span style={{ width: "64%" }} />
                        </span>
                        <span className="home-provider-capacity-value">64% left</span>
                      </div>
                    </div>
                  </div>
                  <div className="home-choice-grid" role="group" aria-label="interaction">
                    <button className="home-choice is-selected" type="button" aria-pressed="true">
                      <MonitorIcon className="home-choice-icon" aria-hidden="true" />
                      <span>gui</span>
                    </button>
                    <button className="home-choice" type="button" aria-pressed="false">
                      <TerminalIcon className="home-choice-icon" aria-hidden="true" />
                      <span>cli</span>
                    </button>
                  </div>

                  <div className="home-panel-head home-panel-subhead">
                    <h3>Initial message</h3>
                    <span className="home-panel-meta">Direct</span>
                  </div>
                  <div className="home-initial-grid" role="group" aria-label="initial message type">
                    <button className="home-model home-initial-option is-selected" type="button" aria-pressed="true">
                      <MessageSquareTextIcon className="home-initial-icon" aria-hidden="true" />
                      <span className="home-initial-main">
                        <span className="home-model-title">Direct</span>
                        <span className="home-model-sub">Send exactly what is typed.</span>
                      </span>
                    </button>
                    <button className="home-model home-initial-option" type="button" aria-pressed="false">
                      <SearchCheckIcon className="home-initial-icon" aria-hidden="true" />
                      <span className="home-initial-main">
                        <span className="home-model-title">Diagnose</span>
                        <span className="home-model-sub">Investigate and report findings first.</span>
                      </span>
                    </button>
                    <button className="home-model home-initial-option" type="button" aria-pressed="false">
                      <BugIcon className="home-initial-icon" aria-hidden="true" />
                      <span className="home-initial-main">
                        <span className="home-model-title">Bug report</span>
                        <span className="home-model-sub">Evidence, architectural miss, and fix plan.</span>
                      </span>
                    </button>
                    <button className="home-model home-initial-option" type="button" aria-pressed="false">
                      <ClipboardListIcon className="home-initial-icon" aria-hidden="true" />
                      <span className="home-initial-main">
                        <span className="home-model-title">Quality gaps</span>
                        <span className="home-model-sub">Audit current policy docs before changing code.</span>
                      </span>
                    </button>
                    <button className="home-model home-initial-option" type="button" aria-pressed="false">
                      <GavelIcon className="home-initial-icon" aria-hidden="true" />
                      <span className="home-initial-main">
                        <span className="home-model-title">Go long</span>
                        <span className="home-model-sub">Heavy solve with binding invariants.</span>
                      </span>
                    </button>
                    <button className="home-model home-initial-option" type="button" aria-pressed="false">
                      <FlaskConicalIcon className="home-initial-icon" aria-hidden="true" />
                      <span className="home-initial-main">
                        <span className="home-model-title">Test skill</span>
                        <span className="home-model-sub">Start by reserving a live validation slot.</span>
                      </span>
                    </button>
                  </div>

                  <div className="home-panel-head home-panel-subhead">
                    <h3>Model</h3>
                    <span className="home-panel-meta">High</span>
                  </div>
                  <div className="home-model-select" data-menu="home-model">
                    <button
                      className="home-model-trigger"
                      type="button"
                      aria-haspopup="listbox"
                      aria-expanded="false"
                    >
                      <span className="home-model-title">Claude · Opus 4.8</span>
                      <svg
                        className="home-model-trigger-icon"
                        viewBox="0 0 16 16"
                        width="14"
                        height="14"
                        fill="none"
                        stroke="currentColor"
                        strokeWidth="2"
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        aria-hidden="true"
                      >
                        <path d="M4 6l4 4 4-4" />
                      </svg>
                    </button>
                  </div>
                  <div className="home-effort-grid" role="group" aria-label="effort">
                    <button className="home-model home-effort" type="button" aria-pressed="false">
                      <span className="home-model-title">Medium</span>
                      <span className="home-model-sub">Balanced latency</span>
                    </button>
                    <button className="home-model home-effort is-selected" type="button" aria-pressed="true">
                      <span className="home-model-title">High</span>
                      <span className="home-model-sub">Deeper reasoning</span>
                    </button>
                  </div>
                </section>

                <section className="home-panel home-panel-actions" aria-labelledby="styleguide-home-actions-title">
                  <div className="home-panel-head">
                    <h3 id="styleguide-home-actions-title">Setup</h3>
                  </div>
                  <div className="home-quick-actions">
                    <button className="home-quick-action" type="button">
                      <IconWrench className="home-quick-icon" />
                      <span className="home-quick-main">
                        <span className="home-quick-title">Claude config</span>
                        <span className="home-quick-sub">Log in once and seed KV for future sessions</span>
                      </span>
                    </button>
                  </div>
                  <div className="home-repos">
                    <div className="home-repos-head">
                      <h3 className="home-repos-title">Workspace repos</h3>
                      <span className="home-repos-meta">2 selected</span>
                    </div>
                    <ul className="home-repos-chips">
                      <li className="home-repos-chip">
                        <span className="home-repos-chip-slug">romaine-life/tank-operator</span>
                        <button className="home-repos-chip-remove" type="button" aria-label="Remove romaine-life/tank-operator">
                          x
                        </button>
                      </li>
                      <li className="home-repos-chip">
                        <span className="home-repos-chip-slug">romaine-life/glimmung</span>
                        <button className="home-repos-chip-remove" type="button" aria-label="Remove romaine-life/glimmung">
                          x
                        </button>
                      </li>
                    </ul>
                    <button className="home-repos-trigger" type="button">
                      + Add repo
                    </button>
                  </div>
                </section>
              </div>
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
