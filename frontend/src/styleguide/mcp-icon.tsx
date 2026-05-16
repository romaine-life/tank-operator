// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import { McpIcon } from "../McpIcon";
import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  rowStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

export function StyleguideMcpIcon() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>mcp icon</h1>
        <p style={captionStyle}>
          Used for MCP server controls and MCP tool calls. The glyph follows
          the Model Context Protocol mark and inherits the surrounding icon
          color.
        </p>
        <section style={sectionStyle}>
          <div style={rowStyle}>
            <button
              type="button"
              className="run-composer-icon-btn"
              aria-label="Show MCP servers"
              title="Show MCP servers"
            >
              <McpIcon className="run-composer-icon" aria-hidden="true" />
            </button>
            <span className="run-tool-icon-glyph tool-color-mcp" aria-hidden="true">
              <McpIcon size={14} strokeWidth={2} />
            </span>
          </div>
        </section>
      </div>
    </div>
  );
}
