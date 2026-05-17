// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import { SquareTerminalIcon } from "lucide-react";
import { McpIcon } from "../McpIcon";
import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  rowStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

export function StyleguideToolIcons() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>tool icons</h1>
        <p style={captionStyle}>
          Transcript tool rows use semantic glyphs instead of relying on the
          rendered tool label.
        </p>
        <section style={sectionStyle}>
          <div style={rowStyle}>
            <span className="run-tool-icon-glyph tool-color-bash" aria-hidden="true">
              <SquareTerminalIcon size={14} strokeWidth={2} />
            </span>
            <span className="run-tool-icon-glyph tool-color-mcp" aria-hidden="true">
              <McpIcon size={14} strokeWidth={2} />
            </span>
          </div>
        </section>
      </div>
    </div>
  );
}
