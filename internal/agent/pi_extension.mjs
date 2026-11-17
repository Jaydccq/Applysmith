// The extension the product loads into pi. It owns no tool: it fetches the
// catalog from the local API, registers each tool with pi and forwards every
// call back, so the guard chain, the tool and the audit stay in the product.
// It is written to the agent directory before each start, so it always
// matches the binary that launched it.

const base = process.env.APPLYSMITH_AGENT_URL;
const token = process.env.APPLYSMITH_AGENT_TOKEN;

async function api(path, init = {}) {
  const res = await fetch(base + path, {
    ...init,
    headers: { authorization: "Bearer " + token, "content-type": "application/json" },
  });
  if (!res.ok) {
    throw new Error(path + ": HTTP " + res.status);
  }
  const envelope = await res.json();
  return envelope.data;
}

// A row whose candidates speak more than one wire is served by several pi
// providers, one per wire, because the gateway never translates between wires.
// The product starts pi on one virtual model that picks among them, and pi's
// own retry moves a failed request to the next: pi replays the conversation
// across providers itself. The product orders the models (the wire of the
// row's first candidate first, a wire whose candidates rest last) and
// describes them in APPLYSMITH_AGENT_ROUTE; without it pi runs on one model.
function registerVirtualModel(pi) {
  if (!process.env.APPLYSMITH_AGENT_ROUTE) {
    return;
  }
  const spec = JSON.parse(process.env.APPLYSMITH_AGENT_ROUTE);
  const key = (m) => m.provider + "/" + m.id;
  // A model that failed a request is not asked again in this process, which is
  // one turn: a retry, a later request and a steering message all skip it.
  const failed = new Set();
  pi.registerVirtualModel({
    provider: spec.provider,
    id: spec.id,
    name: "Applysmith agent",
    thinkingLevels: ["off", "minimal", "low", "medium", "high", "xhigh"],
    route(request, ctx) {
      if (request.reason === "retry" && request.failed) {
        failed.add(key(request.failed.model));
      }
      // Models are tried in order, so the first that has not failed is the one
      // that answered the turn's earlier requests: a tool result stays on it.
      const pick = spec.models.find((m) => !failed.has(key(m)));
      if (!pick) {
        const last = request.failed?.message?.errorMessage;
        throw new Error("Every model of the agent route failed this request." + (last ? " Last error: " + last : ""));
      }
      const model = ctx.modelRegistry.find(pick.provider, pick.id);
      if (!model) {
        throw new Error("Model " + key(pick) + " is not declared.");
      }
      return { model, thinkingLevel: request.thinkingLevel };
    },
  });
}

export default async function (pi) {
  const catalog = await api("/internal/agent/catalog");
  const names = catalog.tools.map((t) => t.name);
  registerVirtualModel(pi);

  // pi runs the calls of one assistant message concurrently but starts their
  // execute functions in the model's order. Chaining them on one promise makes
  // the forwarded requests reach the product one at a time, in that order.
  let tail = Promise.resolve();
  function inOrder(signal, work) {
    const run = tail.then(() => {
      if (signal && signal.aborted) {
        throw new Error("The operation was aborted.");
      }
      return work();
    });
    tail = run.then(
      () => {},
      () => {},
    );
    return run;
  }

  // The instruction block of a skill the model loaded in this turn; a skill a
  // turn starts under is in its system prompt instead.
  let skillBlock = "";

  for (const tool of catalog.tools) {
    pi.registerTool({
      name: tool.name,
      label: tool.label,
      description: tool.description,
      parameters: tool.parameters,
      async execute(toolCallId, params, signal) {
        return inOrder(signal, async () => {
          const answer = await api("/internal/agent/tool-call", {
            method: "POST",
            body: JSON.stringify({ toolCallId, name: tool.name, args: params }),
            signal,
          });
          if (answer.finalOnly) {
            pi.setActiveTools([]);
          } else if (answer.activeTools?.length) {
            pi.setActiveTools(answer.activeTools);
          }
          if (answer.skill) {
            skillBlock = answer.skill;
          }
          if (answer.isError) {
            throw new Error(answer.text);
          }
          const images = (answer.images ?? []).map((i) => ({ type: "image", data: i.data, mimeType: i.mimeType }));
          return { content: [{ type: "text", text: answer.text }, ...images], details: {} };
        });
      },
    });
  }

  let prefix = [];

  pi.on("session_start", async () => {
    await api("/internal/agent/hello", { method: "POST", body: JSON.stringify({ tools: pi.getActiveTools() }) });
  });

  pi.on("before_agent_start", async () => {
    skillBlock = "";
    const turn = await api("/internal/agent/turn-context");
    // A turn that starts under a skill is offered the skill's tools alone.
    pi.setActiveTools(turn.activeTools?.length ? turn.activeTools : names);
    prefix = turn.prefix.map((m) => ({
      role: m.role,
      content: [{ type: "text", text: m.text }],
      timestamp: m.timestamp,
    }));
    return { systemPrompt: turn.systemPrompt };
  });

  // The product packs earlier turns itself; pi's own session starts empty each
  // turn, so what comes before the turn's user message is replaced by that pack.
  pi.on("context", async (event) => {
    const first = event.messages.findIndex((m) => m.role === "user");
    const skill = skillBlock ? [{ role: "user", content: [{ type: "text", text: skillBlock }], timestamp: Date.now() }] : [];
    return { messages: [...prefix, ...skill, ...newestImageOnly(first < 0 ? event.messages : event.messages.slice(first))] };
  });
}

// A screenshot costs the model thousands of tokens on every request that
// carries it, so only the newest tool result keeps its images; an earlier one
// says that it had one.
const hasImage = (m) => m.role === "toolResult" && m.content.some((b) => b.type === "image");

function newestImageOnly(messages) {
  const newest = messages.findLastIndex(hasImage);
  return messages.map((m, i) =>
    i === newest || !hasImage(m)
      ? m
      : {
          ...m,
          content: m.content.map((b) =>
            b.type === "image" ? { type: "text", text: "[An earlier screenshot, left out; look again to see the window now.]" } : b,
          ),
        },
  );
}
