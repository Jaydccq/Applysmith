import repl from 'node:repl';
import { PassThrough, Writable } from 'node:stream';
import { AsyncLocalStorage } from 'node:async_hooks';
import { inspect } from 'node:util';

const MAX_BYTES = 8 * 1024 * 1024;
const methods = new Set([
  'list_apps', 'list_windows', 'get_window', 'launch_app', 'get_app_state',
  'get_window_state', 'activate_window', 'click', 'type_text', 'paste',
  'press_key', 'scroll', 'set_value', 'perform_secondary_action', 'select_text',
  'drag', 'drag_paths', 'debug_tap', 'doctor', 'activate_app',
]);
const scope = new AsyncLocalStorage();
const protocolWrite = process.stdout.write.bind(process.stdout);
const pending = new Map();
let nextCall = 0;
let active = null;
let input = Buffer.alloc(0);

function send(message, flushed) {
  const text = JSON.stringify(message);
  if (Buffer.byteLength(text) > MAX_BYTES) throw new Error('JavaScript result exceeds 8 MiB');
  protocolWrite(text + '\n', flushed);
}

function log(...values) {
  const cell = scope.getStore();
  if (!cell?.active) return;
  const text = values.map(value => typeof value === 'string' ? value : inspect(value, {
    depth: 5, getters: false, customInspect: false, maxArrayLength: 100, maxStringLength: 32768,
  })).join(' ');
  if (text.length === 0) return;
  const bytes = Buffer.byteLength(JSON.stringify(text)) + 1;
  if (cell.logBytes + bytes > MAX_BYTES / 2) { cell.logsTruncated = true; return; }
  cell.logBytes += bytes;
  cell.logs.push(text);
}

// Capture ordinary console/process output. The only protocol writer is send().
for (const stream of [process.stdout, process.stderr]) {
  stream.write = (chunk, encoding, callback) => {
    log(Buffer.isBuffer(chunk) ? chunk.toString('utf8') : String(chunk));
    if (typeof encoding === 'function') encoding();
    else if (typeof callback === 'function') callback();
    return true;
  };
}
const output = new Writable({ write(chunk, _encoding, callback) {
  const text = chunk.toString('utf8');
  const cell = scope.getStore();
  // Default REPL evaluation prints uncaught errors instead of invoking its eval
  // callback. Recover the documented _error value through the output stream.
  if (cell?.active && cell.rejectEvaluation && /^Uncaught(?:[:\s])/.test(text)) {
    const reject = cell.rejectEvaluation;
    cell.rejectEvaluation = null;
    reject(server.context._error ?? new Error(text.trim()));
  } else { log(text); }
  callback();
} });
const server = repl.start({ prompt: '', input: new PassThrough(), output, terminal: false,
  useGlobal: false, ignoreUndefined: true });
server.context.console = Object.freeze(Object.fromEntries(
  ['log', 'info', 'warn', 'error', 'debug', 'dir', 'table'].map(name => [name, log]),
));

function nativeCall(method, args = {}) {
  const cell = scope.getStore();
  if (!cell?.active || !cell.acceptCalls || cell !== active) return Promise.reject(new Error('cu calls require an active js execution'));
  if (args === null || typeof args !== 'object' || Array.isArray(args)) {
    return Promise.reject(new TypeError('cu arguments must be an object'));
  }
  const run = () => new Promise((resolve, reject) => {
    if (!cell.active) { reject(new Error('JavaScript execution ended')); return; }
    const id = String(++nextCall);
    pending.set(id, { resolve, reject, cell });
    try { send({ type: 'call', exec_id: cell.id, id, method, arguments: args }); }
    catch (error) { pending.delete(id); reject(error); }
  });
  const operation = cell.calls.then(run);
  cell.calls = operation.catch(() => {});
  return operation;
}
Object.defineProperty(server.context, 'cu', { value: Object.freeze(Object.fromEntries(
  [...methods].map(method => [method, args => nativeCall(method, args)]),
)), writable: false, configurable: false });

function jsonValue(value) {
  if (value === undefined) return { value_type: 'undefined', value: null };
  const seen = new WeakSet();
  const encoded = JSON.stringify(value, (_key, item) => {
    if (typeof item === 'bigint') return item.toString() + 'n';
    if (typeof item === 'function' || typeof item === 'symbol') return String(item);
    if (item && typeof item === 'object') {
      if (seen.has(item)) return '[Circular or shared reference]';
      seen.add(item);
      if (item instanceof Error) return { name: item.name, message: item.message };
    }
    return item;
  });
  return { value_type: typeof value, value: encoded === undefined ? null : JSON.parse(encoded) };
}

async function execute(message) {
  if (active) { send({ type: 'error', exec_id: message.exec_id, error: 'JavaScript runtime is busy' }); return; }
  if (typeof message.exec_id !== 'string' || typeof message.code !== 'string' || Buffer.byteLength(message.code) > MAX_BYTES) {
    throw new Error('Invalid JavaScript execution request');
  }
  const cell = { id: message.exec_id, active: true, acceptCalls: true, calls: Promise.resolve(), logs: [], logBytes: 0, logsTruncated: false };
  active = cell;
  await scope.run(cell, async () => {
    try {
      const value = await new Promise((resolve, reject) => {
        cell.rejectEvaluation = reject;
        server.eval(message.code + '\n', server.context, 'personal-cu-js', (error, result) => {
          cell.rejectEvaluation = null;
          error ? reject(error) : resolve(result);
        });
      });
      await cell.calls;
      cell.acceptCalls = false;
      send({ type: 'result', exec_id: cell.id, ...jsonValue(value), logs: cell.logs, logs_truncated: cell.logsTruncated });
    } catch (error) {
      await cell.calls;
      cell.acceptCalls = false;
      send({ type: 'error', exec_id: cell.id, error: String(error?.message ?? error), logs: cell.logs });
    } finally {
      cell.active = false;
      active = null;
    }
  });
}

function receive(message) {
  if (message.type === 'exec') {
    execute(message).catch(error => send({ type: 'fatal', error: String(error.message) }, () => process.exit(1)));
  } else if (message.type === 'reply') {
    const entry = pending.get(message.id);
    if (!entry || entry.cell.id !== message.exec_id) throw new Error('Unknown native reply');
    pending.delete(message.id);
    if (message.ok === true) entry.resolve(message.value);
    else entry.reject(new Error(String(message.error ?? 'Native tool failed')));
  } else { throw new Error('Unknown worker message'); }
}
process.on('unhandledRejection', error => log('Unhandled promise rejection:', error));
process.stdin.on('data', chunk => {
  try {
    input = Buffer.concat([input, chunk]);
    for (;;) {
      const newline = input.indexOf(10);
      if (newline < 0) { if (input.length > MAX_BYTES) throw new Error('Input frame exceeds 8 MiB'); break; }
      if (newline > MAX_BYTES) throw new Error('Input frame exceeds 8 MiB');
      const line = input.subarray(0, newline);
      input = input.subarray(newline + 1);
      receive(JSON.parse(line.toString('utf8')));
    }
  } catch (error) { send({ type: 'fatal', error: String(error.message) }, () => process.exit(1)); }
});
process.stdin.on('end', () => process.exit(0));
