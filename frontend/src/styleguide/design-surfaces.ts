export type DesignSurfaceTier =
  | "screen"
  | "pane"
  | "region"
  | "component"
  | "primitive";

export type DesignSurfaceGroup =
  | "workspace"
  | "create"
  | "conversation"
  | "activity"
  | "data"
  | "admin"
  | "debug"
  | "foundation";

export type DesignSurface = {
  name: string;
  tier: DesignSurfaceTier;
  group: DesignSurfaceGroup;
  route?: string;
  source: string;
  description: string;
  aliases?: string[];
};

export const DESIGN_SURFACES = [
  {
    name: "CreateSessionScreen",
    tier: "screen",
    group: "create",
    route: "/new",
    source: "frontend/src/App.tsx",
    description: "Home state for choosing a runtime, repositories, and an optional first prompt before a session exists.",
    aliases: ["home", "splash", "launcher"],
  },
  {
    name: "SessionWorkspaceScreen",
    tier: "screen",
    group: "workspace",
    route: "/sessions/:sessionId",
    source: "frontend/src/App.tsx",
    description: "The primary logged-in work surface: sidebar plus the selected session pane.",
    aliases: ["run", "workspace"],
  },
  {
    name: "SessionTranscriptPane",
    tier: "pane",
    group: "conversation",
    route: "/sessions/:sessionId/transcript",
    source: "frontend/src/App.tsx",
    description: "Durable conversation projection rendered as user and assistant transcript messages.",
    aliases: ["chat", "messages"],
  },
  {
    name: "TurnActivityScreen",
    tier: "screen",
    group: "activity",
    route: "/sessions/:sessionId/turns/:turnNumber?/pages/:pageNumber?",
    source: "frontend/src/App.tsx",
    description: "Per-turn work record for reasoning, tools, shell output, prompts, and paged activity history.",
    aliases: ["turns", "turn detail"],
  },
  {
    name: "StaticPreviewPane",
    tier: "pane",
    group: "data",
    route: "/sessions/:sessionId/static/:path",
    source: "frontend/src/StaticPageView.tsx",
    description: "Sandboxed full-page render for an HTML artifact inside the session workspace.",
  },
  {
    name: "WorkspaceFilesPane",
    tier: "pane",
    group: "data",
    route: "/sessions/:sessionId/files",
    source: "frontend/src/App.tsx",
    description: "Workspace file browser and selected file preview for code, image, and static artifacts.",
    aliases: ["files"],
  },
  {
    name: "SessionDataScreen",
    tier: "screen",
    group: "data",
    route: "/sessions/:sessionId/session-data",
    source: "frontend/src/App.tsx",
    description: "Operational facts for the session: status rows, repositories, clone metadata, and durable identifiers.",
  },
  {
    name: "BackgroundWorkScreen",
    tier: "screen",
    group: "activity",
    route: "/sessions/:sessionId/background",
    source: "frontend/src/App.tsx",
    description: "Long-running background task and scheduled wakeup surface, including stop affordances.",
    aliases: ["background"],
  },
  {
    name: "SettingsScreen",
    tier: "screen",
    group: "admin",
    route: "/settings",
    source: "frontend/src/App.tsx",
    description: "User-level preferences and session defaults outside a single session.",
  },
  {
    name: "AdminControlsScreen",
    tier: "screen",
    group: "admin",
    route: "/settings/admin",
    source: "frontend/src/App.tsx",
    description: "Administrator controls for fleet-level runtime and provider operations.",
  },
  {
    name: "AdminAvatarManagerScreen",
    tier: "screen",
    group: "admin",
    route: "/settings/admin/avatars",
    source: "frontend/src/AdminAvatarManager.tsx",
    description: "Avatar catalog management, upload, crop, kind assignment, and deletion workflow.",
  },
  {
    name: "SessionReportScreen",
    tier: "screen",
    group: "admin",
    route: "/settings/admin/report",
    source: "frontend/src/SessionRepoReport.tsx",
    description: "Per-user repository/session usage report for operational review.",
  },
  {
    name: "ObservabilityScreen",
    tier: "screen",
    group: "admin",
    route: "/settings/admin/observability",
    source: "frontend/src/App.tsx",
    description: "Admin-facing metrics, versions, provider capacity, and runtime evidence.",
  },
  {
    name: "ClusterHealthScreen",
    tier: "screen",
    group: "admin",
    route: "/cluster",
    source: "frontend/src/App.tsx",
    description: "Cluster readiness and health surface for operator-facing diagnosis.",
  },
  {
    name: "HelpScreen",
    tier: "screen",
    group: "workspace",
    route: "/help",
    source: "frontend/src/App.tsx",
    description: "Reference surface for keyboard shortcuts and product help.",
  },
  {
    name: "PublicMessageLinkScreen",
    tier: "screen",
    group: "conversation",
    source: "frontend/src/App.tsx",
    description: "Shared-message read-only view resolved from a durable transcript link.",
  },
  {
    name: "PublicSessionReportScreen",
    tier: "screen",
    group: "admin",
    source: "frontend/src/App.tsx",
    description: "Public report view for a shared session report token.",
  },
  {
    name: "LongChatDebugScreen",
    tier: "screen",
    group: "debug",
    route: "/_debug/long-chat",
    source: "frontend/src/LongChatDebugPage.tsx",
    description: "Synthetic long transcript fixture for scroll, virtualization, and timeline regression work.",
  },
  {
    name: "SessionListDebugScreen",
    tier: "screen",
    group: "debug",
    route: "/_debug/session-list",
    source: "frontend/src/SessionListDebugPage.tsx",
    description: "Session-list ledger/debug capture surface for diagnosing registry and SSE behavior.",
  },
  {
    name: "WorkspaceShell",
    tier: "region",
    group: "workspace",
    source: "frontend/src/WorkspaceShell.tsx",
    description: "Persistent app frame that places the session sidebar next to the active content pane.",
  },
  {
    name: "SessionSidebar",
    tier: "region",
    group: "workspace",
    source: "frontend/src/App.tsx",
    description: "Owned session list, create affordance, profile actions, and cross-session navigation.",
  },
  {
    name: "MobileTopBar",
    tier: "region",
    group: "workspace",
    source: "frontend/src/MobileTopBar.tsx",
    description: "Compact viewport header with navigation drawer controls and session context.",
  },
  {
    name: "RunHeader",
    tier: "region",
    group: "workspace",
    source: "frontend/src/App.tsx",
    description: "Session title row with rename behavior and the overflow action cluster.",
  },
  {
    name: "RunHeaderOverflowMenu",
    tier: "component",
    group: "workspace",
    source: "frontend/src/App.tsx",
    description: "Single menu for secondary session panes and app-level settings/help actions.",
  },
  {
    name: "SessionRow",
    tier: "component",
    group: "workspace",
    source: "frontend/src/App.tsx",
    description: "Sidebar item that names a session and exposes status, unread, mode, and reorder affordances.",
  },
  {
    name: "SessionLauncher",
    tier: "component",
    group: "create",
    source: "frontend/src/App.tsx",
    description: "Create-session form row containing runtime controls, repository selection, and launch action.",
  },
  {
    name: "RepoPicker",
    tier: "component",
    group: "create",
    source: "frontend/src/components/RepoPicker.tsx",
    description: "Repository search, pinning, keyboard selection, and selected-repo preview control.",
  },
  {
    name: "ModeDropdown",
    tier: "component",
    group: "create",
    source: "frontend/src/App.tsx",
    description: "Runtime provider/interaction selector for session creation and defaults.",
  },
  {
    name: "ChatComposer",
    tier: "component",
    group: "conversation",
    source: "frontend/src/ChatComposer.tsx",
    description: "Message composer with attachments, slash tools, quoting, file references, and submit state.",
  },
  {
    name: "TranscriptMessage",
    tier: "component",
    group: "conversation",
    source: "frontend/src/App.tsx",
    description: "Rendered user, assistant, system, or status message within the durable transcript projection.",
  },
  {
    name: "TranscriptMessageActions",
    tier: "component",
    group: "conversation",
    source: "frontend/src/App.tsx",
    description: "Copy, link, quote, fork, and turn-navigation actions attached to transcript messages.",
  },
  {
    name: "QueuedFollowups",
    tier: "component",
    group: "conversation",
    source: "frontend/src/App.tsx",
    description: "Pending local follow-up messages waiting behind the active submitted turn.",
  },
  {
    name: "TurnView",
    tier: "component",
    group: "activity",
    source: "frontend/src/App.tsx",
    description: "Turn detail layout that combines prompt context, activity controls, and work entries.",
  },
  {
    name: "TurnActivityGroup",
    tier: "component",
    group: "activity",
    source: "frontend/src/App.tsx",
    description: "Grouped work entries for a single turn, including tools, thinking, final text, and status.",
  },
  {
    name: "TurnToolGroup",
    tier: "component",
    group: "activity",
    source: "frontend/src/App.tsx",
    description: "Collapsible set of related tool calls within turn activity.",
  },
  {
    name: "TurnToolItem",
    tier: "component",
    group: "activity",
    source: "frontend/src/App.tsx",
    description: "Single tool-call row/body pair with timing, status, input, output, and diff variants.",
  },
  {
    name: "TurnThinkingBubble",
    tier: "component",
    group: "activity",
    source: "frontend/src/App.tsx",
    description: "Live or historical thinking indicator with duration and last-activity metadata.",
  },
  {
    name: "AskUserQuestionCard",
    tier: "component",
    group: "activity",
    source: "frontend/src/App.tsx",
    description: "Inline form for a paused runner turn that needs explicit user input before continuing.",
  },
  {
    name: "BackgroundTaskBlock",
    tier: "component",
    group: "activity",
    source: "frontend/src/App.tsx",
    description: "Background command/task card with state, metadata, output, and stop controls.",
  },
  {
    name: "SessionDataCard",
    tier: "component",
    group: "data",
    source: "frontend/src/App.tsx",
    description: "Reusable information block inside the session data screen.",
  },
  {
    name: "FileCodeViewer",
    tier: "component",
    group: "data",
    source: "frontend/src/FileCodeViewer.tsx",
    description: "Read-only code artifact viewer with language-aware highlighting.",
  },
  {
    name: "FileImageViewer",
    tier: "component",
    group: "data",
    source: "frontend/src/FileImageViewer.tsx",
    description: "Image artifact viewer with fit, zoom, and inspection behavior.",
  },
  {
    name: "AvatarPreviewHost",
    tier: "component",
    group: "admin",
    source: "frontend/src/avatarPreview.tsx",
    description: "Global avatar preview lightbox and edit handoff host.",
  },
  {
    name: "ProviderCapacityStrip",
    tier: "component",
    group: "admin",
    source: "frontend/src/App.tsx",
    description: "Provider quota and rate-limit summary strip used in admin and settings contexts.",
  },
  {
    name: "StatusDot",
    tier: "primitive",
    group: "foundation",
    source: "frontend/src/styleguide/status-dot.tsx",
    description: "Small lifecycle indicator for session and agent states.",
  },
  {
    name: "ModeChip",
    tier: "primitive",
    group: "foundation",
    source: "frontend/src/styleguide/mode-chip.tsx",
    description: "Provider/runtime chip for GUI, CLI, config, and subscription mode display.",
  },
  {
    name: "McpIcon",
    tier: "primitive",
    group: "foundation",
    source: "frontend/src/McpIcon.tsx",
    description: "MCP server/tool-call mark used in tool activity and styleguide specimens.",
  },
  {
    name: "ProviderIcon",
    tier: "primitive",
    group: "foundation",
    source: "frontend/src/providerIcons.tsx",
    description: "Provider logo glyph for Claude, Codex, and related runtime choices.",
  },
  {
    name: "ErrorPill",
    tier: "primitive",
    group: "foundation",
    source: "frontend/src/styleguide/error-pill.tsx",
    description: "Compact inline error label for transient failures and rejected user actions.",
  },
  {
    name: "CopyButton",
    tier: "primitive",
    group: "foundation",
    source: "frontend/src/App.tsx",
    description: "Reusable clipboard action with copied feedback state.",
  },
  {
    name: "LinkButton",
    tier: "primitive",
    group: "foundation",
    source: "frontend/src/App.tsx",
    description: "Reusable deep-link action for messages and shared artifacts.",
  },
  {
    name: "TurnViewButton",
    tier: "primitive",
    group: "foundation",
    source: "frontend/src/App.tsx",
    description: "Jump control from transcript context into the durable turn activity view.",
  },
] as const satisfies readonly DesignSurface[];

export const DESIGN_SURFACE_TIERS = [
  "screen",
  "pane",
  "region",
  "component",
  "primitive",
] as const satisfies readonly DesignSurfaceTier[];

export const DESIGN_SURFACE_GROUPS = [
  "workspace",
  "create",
  "conversation",
  "activity",
  "data",
  "admin",
  "debug",
  "foundation",
] as const satisfies readonly DesignSurfaceGroup[];

export function designSurfacesByGroup(group: DesignSurfaceGroup): DesignSurface[] {
  return DESIGN_SURFACES.filter((surface) => surface.group === group);
}

export function designSurfacesByTier(tier: DesignSurfaceTier): DesignSurface[] {
  return DESIGN_SURFACES.filter((surface) => surface.tier === tier);
}
