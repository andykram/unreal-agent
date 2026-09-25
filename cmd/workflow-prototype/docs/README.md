# Workflow Python API docs

Standalone Astro Starlight documentation for the adjacent prototype.
Requires Node.js 22.12.0 or newer and npm 9.6.5 or newer.

From this directory, install the locked dependencies and start the local site:

```sh
npm ci && npm run dev
```

Open the localhost URL printed by Astro. Stop with Ctrl+C.

```sh
npm run build    # static site in dist/
npm run preview  # serve the production build locally
```

In a sandbox that blocks Astro's preferences directory, use
`ASTRO_TELEMETRY_DISABLED=1 npm run build` or the same prefix with `npm run dev`.

The lockfile pins the full dependency tree. No root package changes are required.
Setup follows the [official Starlight manual setup](https://starlight.astro.build/manual-setup/).

## Maintaining content

Pages live in `src/content/docs/`. Keep tutorials, reference, explanations,
and task guides separate. Update these pages alongside changes to shared `harness/workflow` sources, REPL workflow integration,
`../main.go`, `../run.sh`, and the Python examples.

Distinguish standalone simulation from live REPL execution. Source examples are
imported directly into the annotated guide. Simulator observations do not prove
real provider compatibility or external side-effect recovery.
