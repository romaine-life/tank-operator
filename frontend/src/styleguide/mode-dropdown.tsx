// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import { useState } from "react";
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

export function StyleguideModeDropdown() {
  const [dropdownOpen, setDropdownOpen] = useState(true);

  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>mode dropdown</h1>
        <p style={captionStyle}>
          Provider selection is the only dropdown; action icons stay in the
          launcher row.
        </p>
        <section style={sectionStyle}>
          <div className="new-row new-row-launcher" data-menu="mode">
            <button
              className={`new-row-provider-toggle${dropdownOpen ? " is-open" : ""}`}
              type="button"
              aria-label="choose provider"
              onClick={() => setDropdownOpen((v) => !v)}
            >
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
            {dropdownOpen && (
              <ul className="dropdown dropdown-provider" role="menu">
                <li>
                  <button type="button" aria-label="Claude">
                    <ProviderIcon provider="anthropic" className="dropdown-provider-icon" />
                    <span className="sr-only">Claude</span>
                  </button>
                </li>
                <li>
                  <button type="button" aria-label="Codex">
                    <ProviderIcon provider="codex" className="dropdown-provider-icon" />
                    <span className="sr-only">Codex</span>
                  </button>
                </li>
              </ul>
            )}
          </div>
        </section>
      </div>
    </div>
  );
}
