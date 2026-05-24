import { useEffect, useState } from "react";
import { XIcon } from "lucide-react";
import { authedFetch } from "./auth";

export type AvatarPreviewDetail = {
  name: string;
  avatarSrc: string;
  backingSrc?: string;
  kind?: string;
};

const AVATAR_PREVIEW_EVENT = "tank-avatar-preview";

export function openAvatarPreview(
  detail: AvatarPreviewDetail,
  event?: { stopPropagation?: () => void; preventDefault?: () => void },
) {
  event?.stopPropagation?.();
  event?.preventDefault?.();
  if (typeof window === "undefined") return;
  window.dispatchEvent(new CustomEvent<AvatarPreviewDetail>(AVATAR_PREVIEW_EVENT, { detail }));
}

function displayLabel(kind?: string): string {
  switch (kind) {
    case "agent":
      return "Agent avatar";
    case "system":
      return "System avatar";
    case "personal":
      return "Profile avatar";
    default:
      return "Avatar";
  }
}

async function resolvePreviewSource(detail: AvatarPreviewDetail): Promise<{
  src: string;
  revoke: boolean;
}> {
  const source = detail.backingSrc || detail.avatarSrc;
  if (source.startsWith("/api/")) {
    const res = await authedFetch(source);
    if (!res.ok) throw new Error(`image fetch failed: ${res.status}`);
    return { src: URL.createObjectURL(await res.blob()), revoke: true };
  }
  return { src: source, revoke: false };
}

export function AvatarPreviewHost() {
  const [preview, setPreview] = useState<AvatarPreviewDetail | null>(null);
  const [resolvedSrc, setResolvedSrc] = useState("");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const onPreview = (event: Event) => {
      const detail = (event as CustomEvent<AvatarPreviewDetail>).detail;
      if (!detail?.avatarSrc) return;
      setPreview(detail);
    };
    window.addEventListener(AVATAR_PREVIEW_EVENT, onPreview);
    return () => window.removeEventListener(AVATAR_PREVIEW_EVENT, onPreview);
  }, []);

  useEffect(() => {
    if (!preview) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setPreview(null);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [preview]);

  useEffect(() => {
    if (!preview) {
      setResolvedSrc("");
      setError(null);
      return;
    }
    let cancelled = false;
    let revokeURL = "";
    setError(null);
    setResolvedSrc("");
    void resolvePreviewSource(preview)
      .then(({ src, revoke }) => {
        if (cancelled) {
          if (revoke) URL.revokeObjectURL(src);
          return;
        }
        revokeURL = revoke ? src : "";
        setResolvedSrc(src);
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      });
    return () => {
      cancelled = true;
      if (revokeURL) URL.revokeObjectURL(revokeURL);
    };
  }, [preview]);

  if (!preview) return null;

  return (
    <div className="avatar-lightbox" role="dialog" aria-modal="true" aria-label={preview.name}>
      <button
        type="button"
        className="avatar-lightbox-backdrop"
        aria-label="Close avatar preview"
        onClick={() => setPreview(null)}
      />
      <div className="avatar-lightbox-panel">
        <button
          type="button"
          className="avatar-lightbox-close"
          aria-label="Close avatar preview"
          title="close"
          onClick={() => setPreview(null)}
        >
          <XIcon size={18} aria-hidden="true" />
        </button>
        <div className="avatar-lightbox-media">
          {resolvedSrc ? (
            <img src={resolvedSrc} alt={preview.name} />
          ) : error ? (
            <div className="avatar-lightbox-error">{error}</div>
          ) : (
            <div className="avatar-lightbox-loading">loading...</div>
          )}
          {preview.backingSrc && (
            <img
              className="avatar-lightbox-crop"
              src={preview.avatarSrc}
              alt=""
              aria-hidden="true"
            />
          )}
        </div>
        <div className="avatar-lightbox-caption">
          <span>{preview.name}</span>
          <span>{displayLabel(preview.kind)}</span>
        </div>
      </div>
    </div>
  );
}
