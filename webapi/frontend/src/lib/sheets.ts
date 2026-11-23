// Turning a connected account's spreadsheet into a job table.
//
// The server fetches the cells (it holds the connector's token; this
// page only ever knows a file id the Picker gave it) and hands back a
// rectangle of strings. The only interpretation left is which row is
// the header, which is done here so it can be tested without a browser.
//
// Everything else that could be wrong with a cell is the server's to
// refuse, deliberately: it already owns those rules because it writes
// the queue statement, and a second copy here would be a second place
// for them to drift. A cell holding a space or a comma is fine now --
// the server separates columns with a character no spreadsheet
// produces.

/** One worksheet inside a spreadsheet. */
export interface SheetTab {
  id: number;
  title: string;
}

/** A spreadsheet the user picked, and the tabs it has. */
export interface SheetFile {
  fileId: string;
  name: string;
  tabs: SheetTab[];
}

/** The tab whose cells are currently the job table. */
export interface SheetSelection {
  fileId: string;
  name: string;
  tab: string;
  /** Cells as fetched, header row included. */
  rows: string[][];
  /** The tab had more rows than the server would read. */
  truncated: boolean;
}

const COLUMN_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

/**
 * Split a fetched tab into column names and job rows.
 *
 * The first row is the header, as it is in every spreadsheet anybody
 * keeps parameters in. Its cells become submit variable names, so they
 * have to be usable as one — a column called "sample id" cannot be
 * `$(sample id)`, and saying so beats submitting jobs where the macro
 * expands to nothing.
 *
 * Blank rows are dropped rather than refused. A spreadsheet picks up
 * empty rows the way a document picks up empty paragraphs, and refusing
 * the file over one would be refusing the common case; the count comes
 * back so the page can say how many went.
 *
 * Throws when there is no usable header or no jobs left.
 */
export function splitSheetHeader(
  rows: string[][],
): { columns: string[]; rows: string[][]; skipped: number } {
  if (rows.length === 0) {
    throw new Error('That tab is empty.');
  }
  const header = rows[0].map((c) => c.trim());

  // Trailing empty header cells are an artifact of a wide sheet, not
  // columns. Interior ones are a gap in the header, which is a
  // different thing and is refused below.
  let width = header.length;
  while (width > 0 && header[width - 1] === '') width--;
  if (width === 0) {
    throw new Error(
      'The first row of that tab is empty. It is read as the header, so it needs a name per column.',
    );
  }
  const columns = header.slice(0, width);

  const seen = new Map<string, number>();
  columns.forEach((name, i) => {
    if (name === '') {
      throw new Error(
        `Column ${i + 1} of the header row is empty. Name it, or move the data so the columns are contiguous.`,
      );
    }
    if (!COLUMN_NAME.test(name)) {
      throw new Error(
        `Column ${i + 1} is named "${name}", which cannot be a submit variable: ` +
          `letters, digits and underscore, not starting with a digit. Rename it in the spreadsheet.`,
      );
    }
    // Submit macros are case-insensitive, so two columns differing only
    // in case are one variable and the second would silently win.
    const key = name.toLowerCase();
    const prev = seen.get(key);
    if (prev !== undefined) {
      throw new Error(
        `Columns ${prev + 1} and ${i + 1} are both named "${name}", which is one submit variable.`,
      );
    }
    seen.set(key, i);
  });

  const body: string[][] = [];
  let skipped = 0;
  for (const raw of rows.slice(1)) {
    // Squared off to the header's width: Google omits trailing empty
    // cells, and a wider row means data sitting outside the named
    // columns, which would be silently dropped either way — so it is
    // cut here rather than pretending it was not there.
    const row = Array.from({ length: width }, (_, i) => (raw[i] ?? '').trim());
    if (row.every((c) => c === '')) {
      skipped++;
      continue;
    }
    body.push(row);
  }
  if (body.length === 0) {
    throw new Error(
      'That tab has a header row but no rows of data under it, so there are no jobs to queue.',
    );
  }
  return { columns, rows: body, skipped };
}

/**
 * Where a submission's results go.
 *
 * The reverse direction: the job table reads a spreadsheet, this one
 * writes to it. Each job's declared result file is appended to this tab
 * as the job finishes, fitted to the tab's own header row.
 *
 * `headers` is carried for display only — what the columns actually are
 * at the time a job finishes is read then, from the sheet, so a column
 * added after submitting starts being filled without resubmitting. A
 * copy taken now would go stale and is not what anything decides on.
 */
export interface OutputSheet {
  /** Connector id, as "ns:name". */
  connector: string;
  fileId: string;
  name: string;
  tab: string;
  /** The file each job writes its result to, JSON or CSV. */
  jobFile: string;
  /** The tab's header row as it was when chosen, for showing. */
  headers: string[];
}

/** A file name a job can write and the server will look for. */
const RESULT_FILE = /^[A-Za-z0-9._-]+$/;

/**
 * Check a results file name before it is sent.
 *
 * The server refuses a path too, and its refusal is the one that
 * matters; this exists so the person finds out while typing rather
 * than when the whole submission is rejected. It is deliberately the
 * narrower rule: a name the server would accept but a shell makes
 * awkward is still a bad name to choose here.
 *
 * Returns null when the name is usable.
 */
export function checkResultFileName(name: string): string | null {
  const n = name.trim();
  if (n === '') return 'Name the file each job writes its results to.';
  if (n === '.' || n === '..') return `"${n}" is not a file name.`;
  if (/[/\\]/.test(n)) {
    return 'This is the name of a file in the job’s own directory, not a path.';
  }
  if (!RESULT_FILE.test(n)) {
    return 'Use letters, digits, dots, dashes and underscores.';
  }
  return null;
}
