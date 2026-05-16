// One section per route — pulls a copy of the original section's JSX
// out of the monolithic StyleguideView so feature pages can iterate
// independently. Keep behavior + markup identical to what was inline
// before; this is a pure structural move.

import {
  BackLink,
  captionStyle,
  pageTitleStyle,
  sectionStyle,
  styleguideShellStyle,
  TYPE_SAMPLES,
} from "./shared";

function TypeSample({
  name,
  token,
  size,
  role,
}: {
  name: string;
  token: string;
  size: string;
  role: string;
}) {
  return (
    <div style={{ display: "grid", gap: 4, padding: "10px 0", borderTop: "1px solid var(--border-subtle)" }}>
      <span style={{ fontFamily: "var(--font-primary)", fontSize: `var(${token})`, color: "var(--text-primary)" }}>
        {role}
      </span>
      <span style={{ fontSize: "var(--text-xs)", color: "var(--text-faint)" }}>
        {name} · <code>{token}</code> · {size}
      </span>
    </div>
  );
}

export function StyleguideType() {
  return (
    <div style={styleguideShellStyle}>
      <div style={{ maxWidth: 880 }}>
        <BackLink />
        <h1 style={pageTitleStyle}>type</h1>
        <p style={captionStyle}>
          Archivo carries chrome labels and controls. Mono is reserved for
          terminal output and literal code/path snippets.
        </p>
        <section style={sectionStyle}>
          <div style={{ display: "grid", gap: 0, maxWidth: 620 }}>
            {TYPE_SAMPLES.map(([name, token, size, role]) => (
              <TypeSample key={token} name={name} token={token} size={size} role={role} />
            ))}
            <div style={{ display: "grid", gap: 4, padding: "10px 0", borderTop: "1px solid var(--border-subtle)" }}>
              <span style={{ fontFamily: "var(--font-mono)", fontSize: "var(--text-sm)", color: "var(--text-body)" }}>
                /workspace/tank-operator/frontend/src/App.tsx
              </span>
              <span style={{ fontSize: "var(--text-xs)", color: "var(--text-faint)" }}>
                mono · <code>--font-mono</code> · terminal and literal paths only
              </span>
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
