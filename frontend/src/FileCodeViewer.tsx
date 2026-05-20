import { useEffect, useMemo, useRef } from "react";
import CodeMirror from "@uiw/react-codemirror";
import { EditorSelection, type Extension } from "@codemirror/state";
import {
  Decoration,
  EditorView,
} from "@codemirror/view";
import {
  defaultHighlightStyle,
  StreamLanguage,
  syntaxHighlighting,
} from "@codemirror/language";
import { javascript } from "@codemirror/lang-javascript";
import { json as jsonLanguage } from "@codemirror/lang-json";
import { css as cssLanguage } from "@codemirror/lang-css";
import { html as htmlLanguage } from "@codemirror/lang-html";
import { markdown as markdownLanguage } from "@codemirror/lang-markdown";
import { python as pythonLanguage } from "@codemirror/lang-python";
import { go as goLanguage } from "@codemirror/lang-go";
import { dockerFile } from "@codemirror/legacy-modes/mode/dockerfile";
import { shell } from "@codemirror/legacy-modes/mode/shell";
import { toml } from "@codemirror/legacy-modes/mode/toml";
import { rust } from "@codemirror/legacy-modes/mode/rust";
import { yaml } from "@codemirror/legacy-modes/mode/yaml";

/** Map a filename to a language hint. Viewers fall back to plain text
 *  for unknown languages, which is fine for workspace browsing. */
function syntaxLangForPath(path: string): string {
  const lower = path.toLowerCase();
  const ext = lower.includes(".") ? lower.slice(lower.lastIndexOf(".") + 1) : "";
  const name = lower.slice(lower.lastIndexOf("/") + 1);
  if (name === "dockerfile" || name.startsWith("dockerfile.")) return "dockerfile";
  if (name === "makefile") return "ini";
  if (name === ".gitignore" || name.endsWith(".gitignore")) return "ini";
  if (name.endsWith(".env") || name === ".env") return "ini";
  return (
    {
      ts: "ts",
      tsx: "tsx",
      js: "js",
      jsx: "jsx",
      mjs: "js",
      cjs: "js",
      py: "python",
      go: "go",
      rs: "rust",
      sh: "bash",
      bash: "bash",
      zsh: "bash",
      fish: "bash",
      yml: "yaml",
      yaml: "yaml",
      json: "json",
      jsonc: "json",
      md: "markdown",
      mdx: "markdown",
      html: "html",
      htm: "html",
      xml: "xml",
      svg: "xml",
      css: "css",
      scss: "scss",
      sass: "sass",
      less: "less",
      toml: "toml",
    }[ext] ?? "text"
  );
}

const FILE_CODE_VIEWER_THEME = EditorView.theme({
  "&": {
    height: "100%",
    color: "var(--text-body)",
    backgroundColor: "var(--bg-base, #0a0a0a)",
    fontSize: "0.78rem",
  },
  ".cm-scroller": {
    fontFamily: "var(--font-mono)",
    lineHeight: "1.55",
  },
  ".cm-content": {
    padding: "0.85rem 0",
  },
  ".cm-line": {
    padding: "0 1rem",
  },
  ".cm-gutters": {
    backgroundColor: "var(--bg-base, #0a0a0a)",
    color: "var(--text-muted)",
    borderRight: "1px solid rgba(255, 255, 255, 0.07)",
  },
  ".cm-lineNumbers .cm-gutterElement": {
    padding: "0 0.65rem",
    minWidth: "2.8rem",
  },
  ".cm-activeLine, .cm-activeLineGutter": {
    backgroundColor: "transparent",
  },
  "&.cm-focused": {
    outline: "none",
  },
  ".run-files-cm-target-line": {
    backgroundColor: "rgba(250, 204, 21, 0.16)",
    boxShadow: "inset 3px 0 0 rgba(250, 204, 21, 0.9)",
  },
}, { dark: true });

function codeMirrorLanguageForPath(path: string): Extension[] {
  switch (syntaxLangForPath(path)) {
    case "ts":
      return [javascript({ typescript: true })];
    case "tsx":
      return [javascript({ jsx: true, typescript: true })];
    case "js":
      return [javascript()];
    case "jsx":
      return [javascript({ jsx: true })];
    case "json":
      return [jsonLanguage()];
    case "css":
    case "scss":
    case "sass":
    case "less":
      return [cssLanguage()];
    case "html":
    case "xml":
      return [htmlLanguage()];
    case "markdown":
      return [markdownLanguage()];
    case "python":
      return [pythonLanguage()];
    case "go":
      return [goLanguage()];
    case "rust":
      return [StreamLanguage.define(rust)];
    case "bash":
      return [StreamLanguage.define(shell)];
    case "yaml":
      return [StreamLanguage.define(yaml)];
    case "toml":
      return [StreamLanguage.define(toml)];
    case "dockerfile":
      return [StreamLanguage.define(dockerFile)];
    default:
      return [];
  }
}

function highlightedLineExtension(lineNumber: number | null): Extension[] {
  if (!lineNumber) return [];
  return [
    EditorView.decorations.compute(["doc"], (state) => {
      const clampedLine = Math.max(1, Math.min(lineNumber, state.doc.lines));
      const line = state.doc.line(clampedLine);
      return Decoration.set([
        Decoration.line({ class: "run-files-cm-target-line" }).range(line.from),
      ]);
    }),
  ];
}

export default function FileCodeViewer({
  path,
  value,
  targetLine,
  editable,
  onChange,
}: {
  path: string;
  value: string;
  targetLine: number | null;
  editable: boolean;
  onChange?: (value: string) => void;
}) {
  const viewRef = useRef<EditorView | null>(null);
  const scrolledKeyRef = useRef<string | null>(null);
  const extensions = useMemo<Extension[]>(() => [
    syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
    ...codeMirrorLanguageForPath(path),
    ...highlightedLineExtension(targetLine),
  ], [path, targetLine]);

  useEffect(() => {
    if (!targetLine) return;
    const view = viewRef.current;
    if (!view) return;
    const scrollKey = `${path}:${targetLine}:${editable ? "edit" : "read"}`;
    if (scrolledKeyRef.current === scrollKey) return;
    scrolledKeyRef.current = scrollKey;
    const clampedLine = Math.max(1, Math.min(targetLine, view.state.doc.lines));
    const line = view.state.doc.line(clampedLine);
    const effects = EditorView.scrollIntoView(line.from, { y: "center" });
    if (editable) {
      view.dispatch({
        selection: EditorSelection.cursor(line.from),
        effects,
      });
      view.focus();
      return;
    }
    view.dispatch({ effects });
  }, [editable, path, targetLine, value]);

  return (
    <CodeMirror
      basicSetup={{
        highlightActiveLine: editable,
        highlightActiveLineGutter: editable,
      }}
      className="run-files-code-viewer"
      editable={editable}
      extensions={extensions}
      height="100%"
      onChange={(next) => onChange?.(next)}
      onCreateEditor={(view) => {
        viewRef.current = view;
        if (editable) window.requestAnimationFrame(() => view.focus());
      }}
      readOnly={!editable}
      theme={FILE_CODE_VIEWER_THEME}
      value={value}
    />
  );
}
