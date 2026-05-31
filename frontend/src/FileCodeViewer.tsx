import { useEffect, useMemo, useRef, useState } from "react";
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

async function loadCodeMirrorLanguage(lang: string): Promise<Extension[]> {
  switch (lang) {
    case "ts":
      return [
        (await import("@codemirror/lang-javascript")).javascript({
          typescript: true,
        }),
      ];
    case "tsx":
      return [
        (await import("@codemirror/lang-javascript")).javascript({
          jsx: true,
          typescript: true,
        }),
      ];
    case "js":
      return [(await import("@codemirror/lang-javascript")).javascript()];
    case "jsx":
      return [
        (await import("@codemirror/lang-javascript")).javascript({ jsx: true }),
      ];
    case "json":
      return [(await import("@codemirror/lang-json")).json()];
    case "css":
    case "scss":
    case "sass":
    case "less":
      return [(await import("@codemirror/lang-css")).css()];
    case "html":
    case "xml":
      return [(await import("@codemirror/lang-html")).html()];
    case "markdown":
      return [(await import("@codemirror/lang-markdown")).markdown()];
    case "python":
      return [(await import("@codemirror/lang-python")).python()];
    case "go":
      return [(await import("@codemirror/lang-go")).go()];
    case "rust":
      return [
        StreamLanguage.define(
          (await import("@codemirror/legacy-modes/mode/rust")).rust,
        ),
      ];
    case "bash":
      return [
        StreamLanguage.define(
          (await import("@codemirror/legacy-modes/mode/shell")).shell,
        ),
      ];
    case "yaml":
      return [
        StreamLanguage.define(
          (await import("@codemirror/legacy-modes/mode/yaml")).yaml,
        ),
      ];
    case "toml":
      return [
        StreamLanguage.define(
          (await import("@codemirror/legacy-modes/mode/toml")).toml,
        ),
      ];
    case "dockerfile":
      return [
        StreamLanguage.define(
          (await import("@codemirror/legacy-modes/mode/dockerfile")).dockerFile,
        ),
      ];
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
  const lang = useMemo(() => syntaxLangForPath(path), [path]);
  const [languageExtensions, setLanguageExtensions] = useState<Extension[]>([]);

  useEffect(() => {
    let cancelled = false;
    setLanguageExtensions([]);
    loadCodeMirrorLanguage(lang)
      .then((loadedExtensions) => {
        if (!cancelled) setLanguageExtensions(loadedExtensions);
      })
      .catch(() => {
        if (!cancelled) setLanguageExtensions([]);
      });
    return () => {
      cancelled = true;
    };
  }, [lang]);

  const extensions = useMemo<Extension[]>(() => [
    syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
    ...languageExtensions,
    ...highlightedLineExtension(targetLine),
  ], [languageExtensions, targetLine]);

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
