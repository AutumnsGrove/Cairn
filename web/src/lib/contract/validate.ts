import Ajv2020, { type ValidateFunction } from 'ajv/dist/2020.js';
import addFormats from 'ajv-formats';
import schema from './schema.json';

/**
 * The TypeScript half of the contract validator. It reads the same schema document as the Go side (generated from
 * api/events.schema.json), so a message that validates in a Go test validates in a browser test and the other way
 * round.
 *
 * Only tests import this module, so the schema never reaches the shipped bundle.
 */
const ajv = new Ajv2020({ allErrors: true, strict: false });
addFormats(ajv);

function entryPoint(name: 'serverEvent' | 'clientMessage'): ValidateFunction {
  // The root is a oneOf of both directions, so the sibling keywords are dropped and only the entry point applies.
  // The $id goes too: two entry points compiled from one document must not claim the same schema identity.
  const { oneOf: _oneOf, $id: _id, ...rest } = schema as Record<string, unknown>;
  const compiled = ajv.compile({ ...rest, $ref: `#/$defs/${name}` });
  return compiled as ValidateFunction;
}

export const validateServerEvent = entryPoint('serverEvent');
export const validateClientMessage = entryPoint('clientMessage');

/** Returns null when the event is in the contract, or a readable reason when it is not. */
export function checkServerEvent(value: unknown): string | null {
  return validateServerEvent(value)
    ? null
    : ajv.errorsText(validateServerEvent.errors, { separator: '; ' });
}

/** Returns null when the client message is in the contract, or a readable reason when it is not. */
export function checkClientMessage(value: unknown): string | null {
  return validateClientMessage(value)
    ? null
    : ajv.errorsText(validateClientMessage.errors, { separator: '; ' });
}
