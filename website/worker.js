// Static assets answer a byte-range request with the whole file, and Safari
// plays a video only from ranged (206) responses. wrangler.jsonc sends the
// film's requests here first; everything else is served by the assets alone.
export default {
  async fetch(request, env) {
    const asset = await env.ASSETS.fetch(new Request(request.url, { method: request.method === "HEAD" ? "HEAD" : "GET" }));
    if (!asset.ok) return asset;
    const headers = new Headers(asset.headers);
    headers.set("Accept-Ranges", "bytes");
    const m = /^bytes=(\d*)-(\d*)$/.exec((request.headers.get("Range") || "").trim());
    if (!m || (m[1] === "" && m[2] === "") || request.method === "HEAD") {
      return new Response(asset.body, { status: asset.status, headers });
    }
    const body = await asset.arrayBuffer();
    const size = body.byteLength;
    const start = m[1] === "" ? Math.max(0, size - Number(m[2])) : Number(m[1]);
    const end = m[1] === "" || m[2] === "" ? size - 1 : Math.min(Number(m[2]), size - 1);
    if (start > end) {
      return new Response(null, { status: 416, headers: { "Content-Range": `bytes */${size}` } });
    }
    headers.set("Content-Range", `bytes ${start}-${end}/${size}`);
    headers.set("Content-Length", String(end - start + 1));
    return new Response(body.slice(start, end + 1), { status: 206, headers });
  },
};
