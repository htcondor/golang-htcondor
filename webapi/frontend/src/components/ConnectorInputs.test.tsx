import { describe, expect, it } from 'vitest';
import {
  applyConnectorInputs,
  type ConnectorInput,
} from './ConnectorInputs';

function input(reference: string, name: string): ConnectorInput {
  return { id: reference, reference, name, picker: 'drive' };
}

describe('applyConnectorInputs', () => {
  it('sets transfer_input_files when the body has none', () => {
    const out = applyConnectorInputs('executable = run.sh\n', [
      input('svc+gdrive2://ID/data.csv', 'data.csv'),
    ]);
    expect(out).toContain('transfer_input_files = svc+gdrive2://ID/data.csv');
  });

  // The body's own inputs survive. A template that already lists files
  // is the common case, and clobbering them would send a job without
  // the files it was written to use.
  it('appends to what the body already lists', () => {
    const out = applyConnectorInputs(
      'transfer_input_files = local.dat\n',
      [input('svc+gdrive2://ID/data.csv', 'data.csv')],
    );
    expect(out).toContain('local.dat, svc+gdrive2://ID/data.csv');
  });

  it('adds every chosen file', () => {
    const out = applyConnectorInputs('executable = run.sh\n', [
      input('svc+gdrive2://A/a.csv', 'a.csv'),
      input('svc+gdrive2://B/b.csv', 'b.csv'),
    ]);
    expect(out).toContain('svc+gdrive2://A/a.csv');
    expect(out).toContain('svc+gdrive2://B/b.csv');
  });

  // Nothing chosen must change nothing: the body is what the submitter
  // wrote, and an access point with no chooser configured goes through
  // this same call on every submit.
  it('leaves a body with nothing chosen exactly as it was', () => {
    const body = '# a comment\nexecutable = run.sh\nqueue\n';
    expect(applyConnectorInputs(body, [])).toBe(body);
  });

  it('does not list the same reference twice', () => {
    const ref = input('svc+gdrive2://ID/data.csv', 'data.csv');
    const out = applyConnectorInputs('executable = run.sh\n', [ref, ref]);
    expect(out.match(/svc\+gdrive2:\/\/ID\/data\.csv/g)).toHaveLength(1);
  });
});
