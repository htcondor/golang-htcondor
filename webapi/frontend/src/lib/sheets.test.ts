import { describe, it, expect } from 'vitest';
import { splitSheetHeader } from './sheets';

describe('splitSheetHeader', () => {
  it('reads the first row as the header and the rest as jobs', () => {
    const got = splitSheetHeader([
      ['sample', 'seed'],
      ['HG002', '1'],
      ['HG003', '2'],
    ]);
    expect(got.columns).toEqual(['sample', 'seed']);
    expect(got.rows).toEqual([
      ['HG002', '1'],
      ['HG003', '2'],
    ]);
    expect(got.skipped).toBe(0);
  });

  // The whole point of carrying rows as data: a cell with a space or a
  // comma is ordinary spreadsheet content and must survive untouched.
  // The form this replaced split such a cell into two columns.
  it('keeps a cell containing a space or a comma', () => {
    const got = splitSheetHeader([
      ['sample', 'note'],
      ['HG002 run 3', 'first, second'],
    ]);
    expect(got.rows).toEqual([['HG002 run 3', 'first, second']]);
  });

  it('drops blank rows and counts them', () => {
    const got = splitSheetHeader([
      ['sample'],
      ['a'],
      [''],
      ['b'],
      ['  '],
    ]);
    expect(got.rows).toEqual([['a'], ['b']]);
    expect(got.skipped).toBe(2);
  });

  // Google omits trailing empty cells, so a short row arrives short.
  // Squaring it off is what keeps column 2 of row 2 from being read as
  // column 1.
  it('squares short rows off to the header width', () => {
    const got = splitSheetHeader([
      ['sample', 'seed', 'note'],
      ['a', '1'],
    ]);
    expect(got.rows).toEqual([['a', '1', '']]);
  });

  it('cuts a row wider than the header', () => {
    const got = splitSheetHeader([
      ['sample'],
      ['a', 'stray'],
    ]);
    expect(got.rows).toEqual([['a']]);
  });

  // A wide sheet reports empty columns past the data; they are not
  // columns.
  it('ignores trailing empty header cells', () => {
    const got = splitSheetHeader([
      ['sample', 'seed', '', ''],
      ['a', '1', '', ''],
    ]);
    expect(got.columns).toEqual(['sample', 'seed']);
    expect(got.rows).toEqual([['a', '1']]);
  });

  it('refuses a gap in the header', () => {
    expect(() =>
      splitSheetHeader([
        ['sample', '', 'seed'],
        ['a', 'b', 'c'],
      ]),
    ).toThrow(/Column 2 of the header row is empty/);
  });

  it('refuses a header cell that cannot be a submit variable', () => {
    expect(() =>
      splitSheetHeader([
        ['sample id'],
        ['a'],
      ]),
    ).toThrow(/cannot be a submit variable/);
  });

  // Submit macros are case-insensitive, so these are one variable.
  it('refuses two columns differing only in case', () => {
    expect(() =>
      splitSheetHeader([
        ['sample', 'SAMPLE'],
        ['a', 'b'],
      ]),
    ).toThrow(/one submit variable/);
  });

  it('refuses an empty tab', () => {
    expect(() => splitSheetHeader([])).toThrow(/empty/);
  });

  it('refuses a header with no data under it', () => {
    expect(() => splitSheetHeader([['sample'], ['']])).toThrow(/no rows of data/);
  });

  it('refuses a tab whose first row is blank', () => {
    expect(() => splitSheetHeader([['', ''], ['a', 'b']])).toThrow(
      /first row of that tab is empty/,
    );
  });
});
