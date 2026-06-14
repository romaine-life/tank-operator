// The compact files view is single-column master-detail: on a phone the list and
// the viewer no longer sit side by side, so opening a file swaps the pane to the
// viewer (and a "Files" affordance returns to the list). This module owns the one
// decision behind that — "are we in detail (viewer) mode" — as a pure unit so it
// is gated in CI on its own, independent of the viewer.
//
// Why a unit and not a full render test of the panel: the detail view embeds a
// lazy-loaded CodeMirror editor (FileCodeViewer) that does not render in jsdom,
// and the swap itself is route-driven (/sessions/:id/files vs /files/<path>), so
// both rendered states are validated end-to-end against a live slot. See
// docs/features/artifacts-and-files "Mobile files master-detail".

/** True when the phone files pane should show the viewer instead of the list. */
export function isFilesDetailMode(
  isPhone: boolean,
  hasSelectedFile: boolean,
): boolean {
  return isPhone && hasSelectedFile;
}

/** Body class for the files pane; adds the detail modifier in phone viewer mode. */
export function filesBodyClassName(
  isPhone: boolean,
  hasSelectedFile: boolean,
): string {
  return isFilesDetailMode(isPhone, hasSelectedFile)
    ? "run-files-body run-files-body-detail"
    : "run-files-body";
}
