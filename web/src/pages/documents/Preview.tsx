import { useEffect, useState } from "react";
import { fetchBlob } from "../../api/client";
import { useResource } from "../../hooks";
import { ErrorNote, Loading } from "../../ui/parts";

// PdfPreview shows a stored PDF (a document version, an uploaded résumé) in the
// page. The bytes are fetched with the page's credentials and framed from a
// local address, since a frame pointed at the route could not send the session
// key. The fetch is a resource, so a server that was away answers again by
// itself when it is back. The caller keys it by the file, so another starts afresh.
export function PdfPreview({ path, title }: { path: string; title: string }) {
  const [res, reload] = useResource(() => fetchBlob(path), [path]);
  const [src, setSrc] = useState("");
  const blob = res.data;

  useEffect(() => {
    if (!blob) return;
    const url = URL.createObjectURL(new Blob([blob], { type: "application/pdf" }));
    setSrc(url);
    return () => URL.revokeObjectURL(url);
  }, [blob]);

  if (res.status === "error") return <ErrorNote error={res.error} onRetry={reload} what="the PDF" />;
  if (!src) return <Loading label="Loading the PDF" />;
  return <iframe className="pdf-preview" title={title} src={src} />;
}
