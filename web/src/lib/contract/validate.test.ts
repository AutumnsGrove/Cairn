import { readFileSync, readdirSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { checkClientMessage, checkServerEvent } from './validate';

/**
 * The frontend's half of the contract test: every example in api/examples must validate here too. If this passes
 * and the Go test passes, the two streams can be built apart without meeting.
 */
const examplesDir = new URL('../../../../api/examples/', import.meta.url);

function examples(): { name: string; value: unknown }[] {
  return readdirSync(examplesDir)
    .filter((name) => name.endsWith('.json'))
    .sort()
    .map((name) => ({
      name,
      value: JSON.parse(readFileSync(new URL(name, examplesDir), 'utf8')) as unknown,
    }));
}

describe('the contract', () => {
  const all = examples();

  it('has an example for every message and interaction', () => {
    expect(all.length).toBeGreaterThanOrEqual(30);
  });

  it('accepts every server event', () => {
    for (const { name, value } of all.filter((e) => e.name.startsWith('server-'))) {
      expect(checkServerEvent(value), name).toBeNull();
    }
  });

  it('accepts every client message', () => {
    for (const { name, value } of all.filter((e) => e.name.startsWith('client-'))) {
      expect(checkClientMessage(value), name).toBeNull();
    }
  });

  it('refuses a malformed event', () => {
    const bad = { seq: 1, type: 'text_delta', session_id: 's', ts: '2026-10-31T14:03:22Z', data: {} };
    expect(checkServerEvent(bad)).not.toBeNull();
  });

  it('refuses a message with no client_msg_id', () => {
    expect(checkClientMessage({ type: 'confirm', data: { base_version: 1 } })).not.toBeNull();
  });

  it('tolerates an event type from a newer server, which the client logs and ignores', () => {
    const future = {
      seq: 99,
      type: 'thinking_started',
      session_id: 's',
      ts: '2026-10-31T14:03:22Z',
      data: {},
    };
    // It is not in this build's contract, and that is the point: the client must not crash on it.
    expect(checkServerEvent(future)).not.toBeNull();
  });
});
