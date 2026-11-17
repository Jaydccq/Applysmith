import { get } from "./api/client";
import { remoteMe } from "./api/decoders_system";
import { useResource } from "./hooks";

export type Where = "unknown" | "remote" | "local";

// useWhere says whether this page is open on a paired remote device. A device
// is shown nothing the server would refuse it, so a page that has not heard yet
// shows nothing either; a failed answer is read as local, because a remote
// device that cannot ask is refused everywhere anyway.
export function useWhere(): Where {
  const [me] = useResource(() => get("/api/remote/me", remoteMe), [], [], { keep: "identity.me" });
  if (me.status === "loading") return "unknown";
  if (me.status === "ready" && me.data.kind === "remote-device") return "remote";
  return "local";
}
