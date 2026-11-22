import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  chooseFiles,
  fetchConnectorToken,
  fetchPickers,
  fileReference,
  loadPickerAdapter,
  nameProblem,
  partitionPicked,
  type Picker,
  type PickerAdapter,
} from './picker';

const gdrive: Picker = {
  name: 'drive',
  provider: 'gdrive',
  connector: 'builtin:gdrive',
  service: 'webapi-builtin-gdrive',
  method: 'gdrive2',
  script: '/picker/gdrive.js',
  api_key: 'devkey',
  app_id: '1234',
};

afterEach(() => {
  // restoreAllMocks, not just unstubAllGlobals: a spy on
  // document.createElement that survives the test is bound as the
  // "original" by the next one, which then calls itself.
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  delete window.htcondorPicker;
  document
    .querySelectorAll('script[data-htcondor-picker-adapter]')
    .forEach((el) => el.remove());
});

describe('fileReference', () => {
  it('spells a reference the server parses', () => {
    expect(fileReference(gdrive, { id: 'FILEID', name: 'data.csv' })).toBe(
      'webapi-builtin-gdrive+gdrive2://FILEID/data.csv',
    );
  });

  // The composition comes from the fields the server sent, so a
  // differently-named connector needs no change here.
  it('uses the service and method it was given', () => {
    const other = { ...gdrive, service: 'api2-site-cryoem', method: 'gdrive' };
    expect(fileReference(other, { id: 'X', name: 'a.dat' })).toBe(
      'api2-site-cryoem+gdrive://X/a.dat',
    );
  });
});

describe('nameProblem', () => {
  it('passes the names files actually have', () => {
    for (const name of [
      'data.csv',
      'run-2026-11.tar.gz',
      'final_report.pdf',
      'UPPER.DAT',
      'été.csv',
    ]) {
      expect(nameProblem(name), name).toBeNull();
    }
  });

  // The case the server cannot refuse: this reference would be read as
  // a shorter reference plus a second input file, which is also what
  // those tokens mean when a submitter types them deliberately. This is
  // the only place with the real name in hand.
  it('catches a space, which the server cannot', () => {
    expect(nameProblem('my data.csv')).toMatch(/space/);
  });

  it('catches the other separators and macro expansion', () => {
    expect(nameProblem('a,b.csv')).toMatch(/comma/);
    expect(nameProblem('a;b.csv')).toMatch(/semicolon/);
    expect(nameProblem('$(HOME).csv')).toMatch(/dollar/);
    expect(nameProblem('"quoted".txt')).toMatch(/quote/);
  });

  it('catches a directory reference and a path', () => {
    expect(nameProblem('..')).toMatch(/directory/);
    expect(nameProblem('.')).toMatch(/directory/);
    expect(nameProblem('a/b.csv')).toMatch(/path separator/);
    expect(nameProblem('a\\b.csv')).toMatch(/path separator/);
  });

  it('catches a control character', () => {
    expect(nameProblem('a\nb.csv')).toMatch(/control character/);
  });
});

describe('partitionPicked', () => {
  it('keeps the usable files and says why the rest were not', () => {
    const { usable, rejected } = partitionPicked([
      { id: '1', name: 'good.csv' },
      { id: '2', name: 'bad name.csv' },
    ]);
    expect(usable.map((f) => f.id)).toEqual(['1']);
    expect(rejected).toHaveLength(1);
    expect(rejected[0].file.id).toBe('2');
    expect(rejected[0].problem).toMatch(/space/);
  });

  // Picking three files of which one is unusable should add two, not
  // refuse all three: the person can rename one file without losing
  // the choices they already made.
  it('does not discard the good ones along with the bad', () => {
    const { usable } = partitionPicked([
      { id: '1', name: 'a.csv' },
      { id: '2', name: 'b c.csv' },
      { id: '3', name: 'd.csv' },
    ]);
    expect(usable.map((f) => f.id)).toEqual(['1', '3']);
  });
});

describe('fetchPickers', () => {
  it('returns what the access point offers', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => [gdrive],
      }),
    );
    await expect(fetchPickers()).resolves.toEqual([gdrive]);
  });

  // An access point with no chooser configured has no such route, and
  // that is not a failure to show somebody trying to submit a job.
  it('is quiet when no chooser is configured', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 404 }));
    await expect(fetchPickers()).resolves.toEqual([]);
  });
});

describe('fetchConnectorToken', () => {
  it('reads a token', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({ access_token: 'ya29.x', expires_in: 3599 }),
      }),
    );
    await expect(fetchConnectorToken('builtin:gdrive')).resolves.toEqual({
      connected: true,
      token: 'ya29.x',
    });
  });

  // Nobody has consented the first time they use a chooser. That is an
  // outcome with somewhere to go, not an error.
  it('treats not-connected as an answer', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: false,
        status: 409,
        json: async () => ({
          connected: false,
          connect_url: '/api/v1/connect/builtin:gdrive',
        }),
      }),
    );
    const out = await fetchConnectorToken('builtin:gdrive');
    expect(out).toEqual({
      connected: false,
      connectUrl: '/api/v1/connect/builtin:gdrive',
      needsConsent: false,
    });
  });

  it('distinguishes a grant that has since died', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: false,
        status: 409,
        json: async () => ({
          connect_url: '/api/v1/connect/builtin:gdrive',
          needs_consent: true,
        }),
      }),
    );
    const out = await fetchConnectorToken('builtin:gdrive');
    expect(out).toMatchObject({ connected: false, needsConsent: true });
  });

  it('reports what the server said on a real failure', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: false,
        status: 502,
        json: async () => ({ message: 'The provider could not be reached' }),
      }),
    );
    await expect(fetchConnectorToken('builtin:gdrive')).rejects.toThrow(
      /could not be reached/,
    );
  });

  // A 200 with no token would otherwise hand `undefined` to the chooser,
  // which renders and lists nothing.
  it('refuses a token-less success', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({}) }),
    );
    await expect(fetchConnectorToken('builtin:gdrive')).rejects.toThrow(/no token/);
  });

  it('escapes the connector id in the path', async () => {
    const f = vi
      .fn()
      .mockResolvedValue({ ok: true, status: 200, json: async () => ({ access_token: 't' }) });
    vi.stubGlobal('fetch', f);
    await fetchConnectorToken('site:my drive');
    expect(f.mock.calls[0][0]).toBe('/api/v1/connect/site%3Amy%20drive/token');
  });
});

// --- the adapter ------------------------------------------------------

/** Install a stand-in adapter the way the served one does. */
function stubAdapter(impl: PickerAdapter) {
  const original = document.createElement.bind(document);
  vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
    const el = original(tag) as HTMLScriptElement;
    if (tag === 'script') {
      // Loading is what defines the global, so do both together.
      setTimeout(() => {
        window.htcondorPicker = impl;
        el.dispatchEvent(new Event('load'));
      }, 0);
    }
    return el;
  });
}

describe('loadPickerAdapter', () => {
  it('loads an adapter once, however many times it is asked for', async () => {
    const impl: PickerAdapter = { open: vi.fn() };
    stubAdapter(impl);
    const [a, b] = await Promise.all([
      loadPickerAdapter('/picker/gdrive.js'),
      loadPickerAdapter('/picker/gdrive.js'),
    ]);
    expect(a).toBe(impl);
    expect(b).toBe(impl);
    expect(
      document.querySelectorAll('script[data-htcondor-picker-adapter]'),
    ).toHaveLength(1);
  });

  it('says the policy may be the reason it could not load', async () => {
    const original = document.createElement.bind(document);
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = original(tag) as HTMLScriptElement;
      if (tag === 'script') {
        setTimeout(() => el.dispatchEvent(new Event('error')), 0);
      }
      return el;
    });
    await expect(loadPickerAdapter('/picker/nope.js')).rejects.toThrow(
      /Content-Security-Policy/,
    );
  });

  // A chooser that failed once because the network was down has to be
  // openable again, so a rejection must not be what the cache holds.
  it('does not remember a failure', async () => {
    let attempts = 0;
    const impl: PickerAdapter = { open: vi.fn() };
    const original = document.createElement.bind(document);
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = original(tag) as HTMLScriptElement;
      if (tag === 'script') {
        attempts += 1;
        const first = attempts === 1;
        setTimeout(() => {
          if (first) {
            el.dispatchEvent(new Event('error'));
          } else {
            window.htcondorPicker = impl;
            el.dispatchEvent(new Event('load'));
          }
        }, 0);
      }
      return el;
    });
    await expect(loadPickerAdapter('/picker/flaky.js')).rejects.toThrow();
    await expect(loadPickerAdapter('/picker/flaky.js')).resolves.toBe(impl);
  });

  it('refuses an adapter that defines no chooser', async () => {
    const original = document.createElement.bind(document);
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = original(tag) as HTMLScriptElement;
      if (tag === 'script') {
        setTimeout(() => el.dispatchEvent(new Event('load')), 0);
      }
      return el;
    });
    await expect(loadPickerAdapter('/picker/empty.js')).rejects.toThrow(
      /defined no chooser/,
    );
  });
});

describe('chooseFiles', () => {
  function withToken() {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({ access_token: 'ya29.x' }),
      }),
    );
  }

  it('hands the chooser the token and the provider keys', async () => {
    withToken();
    const open = vi.fn((opts) => opts.onPicked([{ id: 'F', name: 'a.csv' }]));
    stubAdapter({ open });
    const out = await chooseFiles({ ...gdrive, script: '/picker/t1.js' });
    expect(out).toEqual({ kind: 'picked', files: [{ id: 'F', name: 'a.csv' }] });
    expect(open.mock.calls[0][0]).toMatchObject({
      token: 'ya29.x',
      apiKey: 'devkey',
      appId: '1234',
    });
  });

  it('reports cancelling as an outcome, not an error', async () => {
    withToken();
    stubAdapter({ open: (opts) => opts.onCancel?.() });
    await expect(
      chooseFiles({ ...gdrive, script: '/picker/t2.js' }),
    ).resolves.toEqual({ kind: 'cancelled' });
  });

  // Without a grant there is no token, so the chooser is never opened
  // and the person is sent to consent instead.
  it('asks for consent rather than opening an empty chooser', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: false,
        status: 409,
        json: async () => ({ connect_url: '/api/v1/connect/builtin:gdrive' }),
      }),
    );
    const open = vi.fn();
    stubAdapter({ open });
    const out = await chooseFiles({ ...gdrive, script: '/picker/t3.js' });
    expect(out).toMatchObject({ kind: 'needs-connect' });
    expect(open).not.toHaveBeenCalled();
  });

  it('surfaces what the chooser said went wrong', async () => {
    withToken();
    stubAdapter({ open: (opts) => opts.onError?.('the picker module failed') });
    await expect(
      chooseFiles({ ...gdrive, script: '/picker/t4.js' }),
    ).rejects.toThrow(/picker module failed/);
  });

  // A chooser that calls back twice must not settle twice: Google's
  // fires a callback per action, and a late CANCEL after a PICKED
  // would otherwise discard the files.
  it('keeps the first outcome', async () => {
    withToken();
    stubAdapter({
      open: (opts) => {
        opts.onPicked([{ id: 'F', name: 'a.csv' }]);
        opts.onCancel?.();
        opts.onError?.('too late');
      },
    });
    await expect(
      chooseFiles({ ...gdrive, script: '/picker/t5.js' }),
    ).resolves.toEqual({ kind: 'picked', files: [{ id: 'F', name: 'a.csv' }] });
  });
});
