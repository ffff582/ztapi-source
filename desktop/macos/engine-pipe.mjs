const frameLimit = 2 * 1024 * 1024;
const maximum = 32;
const unconfirmed = () => Error('ENGINE_PIPE_UNCONFIRMED');

async function* frames(input) {
  let chunks = [], size = 0;
  for await (const value of input) {
    const part = Buffer.from(value);
    let offset = 0;
    while (offset < part.length) {
      const newline = part.indexOf(10, offset);
      const end = newline < 0 ? part.length : newline;
      size += end - offset;
      if (size > frameLimit) throw unconfirmed();
      chunks.push(part.subarray(offset, end));
      if (newline < 0) break;
      if (!size) throw unconfirmed();
      let frame;
      try {
        const text = new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(chunks, size));
        frame = JSON.parse(text);
      } catch { throw unconfirmed(); }
      if (!frame || typeof frame !== 'object' || Array.isArray(frame)) throw unconfirmed();
      yield frame;
      offset = end + 1;
      chunks = [];
      size = 0;
    }
  }
  if (size) throw unconfirmed();
}

function validRequest({ channel, body }) {
  return ['model', 'projectTool'].includes(channel) && typeof body === 'string' &&
    Buffer.byteLength(body) <= (channel === 'model' ? 131072 : 65536);
}

function send(output, value) {
  const line = Buffer.from(JSON.stringify(value) + '\n');
  if (line.length > frameLimit) throw unconfirmed();
  return new Promise((resolve, reject) => {
    output.write(line, error => error ? reject(unconfirmed()) : resolve());
  });
}

export function createEnginePipeClient(input, output) {
  const replies = frames(input);
  let tail = Promise.resolve(), seq = 0, failed = false;
  return (request, signal) => {
    const pending = tail.then(async () => {
      if (failed || !request || !validRequest(request) || seq >= maximum) throw unconfirmed();
      signal?.throwIfAborted();
      const id = ++seq;
      try {
        await send(output, { id, channel: request.channel, body: request.body });
        const { value, done } = await replies.next();
        if (done || value.id !== id || !value.result || Object.keys(value).length !== 2) throw unconfirmed();
        signal?.throwIfAborted();
        return value.result;
      } catch (err) {
        failed = true;
        throw err;
      }
    });
    tail = pending.catch(() => {});
    return pending;
  };
}

export async function serveEnginePipe(input, output, dispatch) {
  let expected = 1;
  for await (const frame of frames(input)) {
    if (expected > maximum || frame.id !== expected || !validRequest(frame) || Object.keys(frame).length !== 3)
      throw unconfirmed();
    const result = await dispatch(Object.freeze({ channel: frame.channel, body: frame.body }));
    await send(output, { id: expected++, result });
  }
  return expected - 1;
}
