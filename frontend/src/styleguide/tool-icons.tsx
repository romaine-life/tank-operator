// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import {
  BotIcon,
  CameraIcon,
  ClipboardListIcon,
  FileDiffIcon,
  FileTextIcon,
  GlobeIcon,
  ListChecksIcon,
  MessageSquareIcon,
  NotebookPenIcon,
  PlayIcon,
  SearchIcon,
  SquarePenIcon,
  SquareTerminalIcon,
  TimerIcon,
  type LucideIcon,
} from "lucide-react";
import { McpIcon } from "../McpIcon";
import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  rowStyle,
  sectionStyle,
  styleguideShellStyle,
} from "./shared";

const TOOL_ICON_SAMPLES: { Icon: LucideIcon; colorClass: string; label: string }[] = [
  { Icon: SquareTerminalIcon, colorClass: "tool-color-bash", label: "shell" },
  { Icon: FileTextIcon, colorClass: "tool-color-read", label: "read" },
  { Icon: SquarePenIcon, colorClass: "tool-color-edit", label: "edit" },
  { Icon: FileDiffIcon, colorClass: "tool-color-edit", label: "diff" },
  { Icon: SearchIcon, colorClass: "tool-color-search", label: "search" },
  { Icon: GlobeIcon, colorClass: "tool-color-search", label: "web" },
  { Icon: CameraIcon, colorClass: "tool-color-search", label: "screenshot" },
  { Icon: ListChecksIcon, colorClass: "tool-color-todo", label: "todo" },
  { Icon: MessageSquareIcon, colorClass: "tool-color-todo", label: "ask" },
  { Icon: BotIcon, colorClass: "tool-color-task", label: "task" },
  { Icon: ClipboardListIcon, colorClass: "tool-color-plan", label: "plan" },
  { Icon: TimerIcon, colorClass: "tool-color-plan", label: "wakeup" },
  { Icon: PlayIcon, colorClass: "tool-color-plan", label: "remote" },
  { Icon: NotebookPenIcon, colorClass: "tool-color-edit", label: "notebook" },
];

export function StyleguideToolIcons() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>tool icons</h1>
        <p style={captionStyle}>
          Transcript tool rows use semantic glyphs for common tool families,
          then fall back to a generic tool color only when no mapping exists.
        </p>
        <section style={sectionStyle}>
          <div style={rowStyle}>
            {TOOL_ICON_SAMPLES.map(({ Icon, colorClass, label }) => (
              <span
                key={String(label)}
                style={{ display: "inline-flex", alignItems: "center", gap: 6 }}
              >
                <span className={`run-tool-icon-glyph ${colorClass}`} aria-hidden="true">
                  <Icon size={14} strokeWidth={2} />
                </span>
                <span style={{ color: "var(--text-muted)", fontSize: "var(--text-xs)" }}>{label}</span>
              </span>
            ))}
            <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
              <span className="run-tool-icon-glyph tool-color-mcp" aria-hidden="true">
                <McpIcon size={14} strokeWidth={2} />
              </span>
              <span style={{ color: "var(--text-muted)", fontSize: "var(--text-xs)" }}>mcp</span>
            </span>
          </div>
        </section>
      </div>
    </div>
  );
}
