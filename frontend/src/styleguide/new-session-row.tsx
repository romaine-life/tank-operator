// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import { ProviderIcon } from "../providerIcons";
import {
  BackLink,
  captionStyle,
  IconChevronDown,
  IconKey,
  IconWrench,
  pageTitleStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

export function StyleguideNewSessionRow() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>new session row</h1>
        <p style={captionStyle}>
          Provider selector first, then default session, API-key fallback,
          and provider-specific config.
        </p>
        <section style={sectionStyle}>
          <div className="new-row new-row-launcher" data-menu="mode">
            <button className="new-row-provider-toggle" type="button" aria-label="choose provider">
              <span className="new-row-provider-slot">
                <ProviderIcon provider="anthropic" className="new-row-provider-icon" />
              </span>
              <IconChevronDown className="new-row-provider-chevron" />
            </button>
            <div className="new-row-action-group" role="group" aria-label="session actions">
              <button className="new-row-action" type="button" aria-label="start default session">
                <span className="row-icon">+</span>
              </button>
              <button className="new-row-action" type="button" aria-label="start API key session">
                <IconKey className="new-row-action-icon" />
              </button>
              <button className="new-row-action" type="button" aria-label="start config session">
                <IconWrench className="new-row-action-icon" />
              </button>
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
