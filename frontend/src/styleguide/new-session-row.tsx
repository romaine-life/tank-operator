import {
  ClipboardListIcon,
  FlaskConicalIcon,
  MessageSquareTextIcon,
  MonitorIcon,
  SearchCheckIcon,
  TerminalIcon,
} from "lucide-react";
import { ProviderIcon } from "../providerIcons";
import {
  BackLink,
  captionStyle,
  IconKey,
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
                    <button className="home-choice is-selected" type="button" aria-pressed="true" title="Claude GUI">
                      <ProviderIcon provider="anthropic" className="home-choice-icon" />
                      <span>Claude</span>
                    </button>
                    <button className="home-choice" type="button" aria-pressed="false" title="Codex GUI">
                      <ProviderIcon provider="codex" className="home-choice-icon" />
                      <span>Codex</span>
                    </button>
                    <button className="home-choice" type="button" aria-pressed="false" title="Hermes">
                      <ProviderIcon provider="hermes" className="home-choice-icon" />
                      <span>Hermes</span>
                    </button>
                    <button className="home-choice" type="button" aria-pressed="false" title="Pi CLI">
                      <ProviderIcon provider="pi" className="home-choice-icon" />
                      <span>Pi</span>
                    </button>
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
                      <ClipboardListIcon className="home-initial-icon" aria-hidden="true" />
                      <span className="home-initial-main">
                        <span className="home-model-title">Quality gaps</span>
                        <span className="home-model-sub">Audit current policy docs before changing code.</span>
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
                  <div className="home-model-list" role="group" aria-label="model">
                    <button className="home-model is-selected" type="button" aria-pressed="true">
                      <span className="home-model-title">Claude Opus 4.8</span>
                    </button>
                    <button className="home-model" type="button" aria-pressed="false">
                      <span className="home-model-title">Claude Opus 4.7</span>
                    </button>
                    <button className="home-model" type="button" aria-pressed="false">
                      <span className="home-model-title">Claude Sonnet 4.5</span>
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
                      <IconKey className="home-quick-icon" />
                      <span className="home-quick-main">
                        <span className="home-quick-title">API key</span>
                        <span className="home-quick-sub">Specify an API key fallback</span>
                      </span>
                    </button>
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
                        <span className="home-repos-chip-slug">nelsong6/tank-operator</span>
                        <button className="home-repos-chip-remove" type="button" aria-label="Remove nelsong6/tank-operator">
                          x
                        </button>
                      </li>
                      <li className="home-repos-chip">
                        <span className="home-repos-chip-slug">nelsong6/glimmung</span>
                        <button className="home-repos-chip-remove" type="button" aria-label="Remove nelsong6/glimmung">
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
