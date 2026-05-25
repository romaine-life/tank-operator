import { MonitorIcon, TerminalIcon } from "lucide-react";
import { ProviderIcon } from "../providerIcons";
import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

export function StyleguideModeDropdown() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>runtime controls</h1>
        <p style={captionStyle}>
          Runtime selection now lives in the home configuration panel: provider
          buttons first, then GUI/CLI interaction buttons. The old compact
          provider dropdown is retired.
        </p>
        <section style={sectionStyle}>
          <div className="home-panel" style={{ maxWidth: 620 }}>
            <div className="home-panel-head">
              <h3>Provider</h3>
              <span className="home-panel-meta">Codex GUI</span>
            </div>
            <div className="home-choice-grid" role="group" aria-label="provider">
              <button className="home-choice" type="button" aria-pressed="false" title="Claude GUI">
                <ProviderIcon provider="anthropic" className="home-choice-icon" />
                <span>Claude</span>
              </button>
              <button className="home-choice is-selected" type="button" aria-pressed="true" title="Codex GUI">
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
            <div className="home-panel-head home-panel-subhead">
              <h3>Interaction</h3>
              <span className="home-panel-meta">gui</span>
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
              <h3>Unavailable state</h3>
              <span className="home-panel-meta">Hermes CLI</span>
            </div>
            <div className="home-choice-grid" role="group" aria-label="unavailable interaction">
              <button className="home-choice is-selected" type="button" aria-pressed="true">
                <ProviderIcon provider="hermes" className="home-choice-icon" />
                <span>Hermes</span>
              </button>
              <button className="home-choice is-selected" type="button" aria-pressed="true">
                <MonitorIcon className="home-choice-icon" aria-hidden="true" />
                <span>gui</span>
              </button>
              <button className="home-choice" type="button" disabled title="not available for this provider">
                <TerminalIcon className="home-choice-icon" aria-hidden="true" />
                <span>cli</span>
              </button>
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
