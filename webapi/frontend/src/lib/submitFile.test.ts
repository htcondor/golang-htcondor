import { describe, expect, it } from 'vitest';
import {
  buildConnectorTable,
  buildTableQueue,
  CONNECTOR_INPUT_MACRO,
  ensureTransferInput,
} from './submitFile';

describe('buildConnectorTable', () => {
  const files = [
    { name: 'a.csv', reference: 'webapi-builtin-gdrive+gdrive2://1AbC/a.csv' },
    { name: 'b.csv', reference: 'webapi-builtin-gdrive+gdrive2://2DeF/b.csv' },
  ];

  // One job per picked file, and the filename lands where a template
  // written for the directory-upload mode already expects it.
  it('gives each file a row, with the name in the first column', () => {
    const { columns, rows } = buildConnectorTable(['name'], files);
    expect(columns).toEqual(['name', 'connector_input']);
    expect(rows).toEqual([
      ['a.csv', 'webapi-builtin-gdrive+gdrive2://1AbC/a.csv'],
      ['b.csv', 'webapi-builtin-gdrive+gdrive2://2DeF/b.csv'],
    ]);
  });

  it('leaves the template\u2019s other columns empty for the submitter to fill', () => {
    const { columns, rows } = buildConnectorTable(['name', 'seed'], files);
    expect(columns).toEqual(['name', 'seed', 'connector_input']);
    expect(rows[0]).toEqual([
      'a.csv',
      '',
      'webapi-builtin-gdrive+gdrive2://1AbC/a.csv',
    ]);
  });

  // A template with no variables of its own is still one job per file.
  it('queues on the reference alone when the template has no columns', () => {
    const { columns, rows } = buildConnectorTable([], files);
    expect(columns).toEqual(['connector_input']);
    expect(rows).toEqual([
      ['webapi-builtin-gdrive+gdrive2://1AbC/a.csv'],
      ['webapi-builtin-gdrive+gdrive2://2DeF/b.csv'],
    ]);
  });

  // Silently picking a winner would either drop the submitter's column
  // or drop the file, and the submit file would look correct either way.
  it('refuses a template that already uses the reserved column name', () => {
    expect(() => buildConnectorTable(['connector_input'], files)).toThrow(
      /already has a column named/,
    );
  });

  // The screen on picked names lives upstream; this is the backstop,
  // because the failure it prevents is a submit file that means
  // something different rather than one that errors.
  it('refuses a reference containing whitespace', () => {
    expect(() =>
      buildConnectorTable(['name'], [
        { name: 'two words.csv', reference: 'svc+m://ID/two words.csv' },
      ]),
    ).toThrow(/whitespace/);
  });

  // The whole point: what the generated submit file actually says.
  it('produces a queue statement the access point can act on', () => {
    const { columns, rows } = buildConnectorTable(['name'], files);
    const queue = buildTableQueue(columns, rows);
    expect(queue).toBe(
      'queue name, connector_input in (a.csv webapi-builtin-gdrive+gdrive2://1AbC/a.csv, ' +
        'b.csv webapi-builtin-gdrive+gdrive2://2DeF/b.csv)',
    );
    const body = ensureTransferInput('executable = run.sh\n', CONNECTOR_INPUT_MACRO);
    expect(body).toContain('transfer_input_files = $(connector_input)');
  });
});
