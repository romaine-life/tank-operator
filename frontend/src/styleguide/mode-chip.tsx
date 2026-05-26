// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import { ProviderIcon } from "../providerIcons";
import { MonitorIcon, TerminalIcon } from "lucide-react";
import {
  BackLink,
  captionStyle,
  MODE_FULL_LABELS,
  MODE_ICONS,
  MODE_INTERACTIONS,
  MODE_LABELS,
  MODES,
  pageTitleStyle,
  rowStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

export function StyleguideModeChip() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>mode chip</h1>
        <p style={captionStyle}>
          Surfaces the auth mode (Claude / API / config) on the
          session row. Provider-backed modes render as a provider chip plus a
          GUI/CLI interaction chip, matching the current sidebar.
        </p>
        <section style={sectionStyle}>
          <div style={rowStyle}>
            {MODES.map((m) => (
              <span key={m} style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
                <span
                  className={`mode mode-${m}${MODE_ICONS[m] ? " mode-icon-only" : ""}`}
                  title={MODE_FULL_LABELS[m]}
                  aria-label={MODE_FULL_LABELS[m]}
                >
                  {MODE_ICONS[m] ? (
                    <>
                      <ProviderIcon provider={MODE_ICONS[m]} className="mode-provider-icon" />
                      <span className="sr-only">{MODE_LABELS[m]}</span>
                    </>
                  ) : (
                    MODE_LABELS[m]
                  )}
                </span>
                {MODE_INTERACTIONS[m] && (
                  <span
                    className="mode mode-icon-only mode-interaction-chip"
                    title={MODE_INTERACTIONS[m]}
                    aria-label={MODE_INTERACTIONS[m]}
                  >
                    {MODE_INTERACTIONS[m] === "gui" ? (
                      <MonitorIcon className="mode-interaction-icon" aria-hidden="true" />
                    ) : (
                      <TerminalIcon className="mode-interaction-icon" aria-hidden="true" />
                    )}
                  </span>
                )}
              </span>
            ))}
          </div>
        </section>
      </div>
    </div>
  );
}
