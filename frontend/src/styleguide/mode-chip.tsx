// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import { ProviderIcon } from "../providerIcons";
import {
  BackLink,
  captionStyle,
  MODE_FULL_LABELS,
  MODE_ICONS,
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
          session row. Each rides its own tinted background — not bordered
          pills — so the row is calm at rest but legible at a glance.
        </p>
        <section style={sectionStyle}>
          <div style={rowStyle}>
            {MODES.map((m) => (
              <span
                key={m}
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
            ))}
          </div>
        </section>
      </div>
    </div>
  );
}
