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

## Publish to GitHub Pages

The `Publish workflow documentation` action publishes from `main` when the docs,
imported examples, or publishing workflow change. It also supports manual dispatch
on `main`. Pull requests do not deploy. Builds use `npm ci` and the committed lockfile.

Enable the repository's Pages source as GitHub Actions once. Repository administrators
can create that configuration with the CLI, or update an existing Pages site:

```sh
# New Pages site:
gh api --method POST repos/OWNER/REPO/pages -f build_type=workflow
# Existing Pages site:
gh api --method PUT repos/OWNER/REPO/pages -f build_type=workflow
```

Keep the `github-pages` environment restricted to `main`. Deployment requires
`pages: write` and `id-token: write`; the build job only reads contents and Pages
metadata. No personal access token is stored in the workflow.

For `andykram/unreal-agent`, the project site is
<https://andykram.github.io/unreal-agent/>. The workflow reads the actual origin
and base path from Pages metadata, so forks and configured custom domains use
their own URLs. Local development stays at `/`.

To reproduce the project-site build locally:

```sh
ASTRO_SITE=https://andykram.github.io ASTRO_BASE=/unreal-agent \
  ASTRO_TELEMETRY_DISABLED=1 npm run build
```

Markdown root-relative links receive the configured base through
`src/base-links.mjs`. Starlight applies it to navigation and assets.
Keep raw HTML links base-aware when adding them.

See [Astro's GitHub Pages guide](https://docs.astro.build/en/guides/deploy/github/)
and [GitHub's custom workflow requirements](https://docs.github.com/en/pages/getting-started-with-github-pages/using-custom-workflows-with-github-pages).
