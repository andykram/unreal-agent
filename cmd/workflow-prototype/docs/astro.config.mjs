import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  integrations: [starlight({
    title: 'Workflow Python API',
    description: 'Author workflow graphs with Python and explore their simulated execution in Go.',
    sidebar: [
      { label: 'Overview', slug: '' },
      { label: 'Tutorials', items: [
        { label: 'Build your first workflow', slug: 'tutorials/quickstart' },
        { label: 'Explore branches and repairs', slug: 'tutorials/branches-and-repairs' },
        { label: 'Explore structured agent output', slug: 'tutorials/structured-output' },
        { label: 'Resume a workflow', slug: 'tutorials/resume-workflow' },
      ] },
      { label: 'Reference', items: [
        { label: 'Python API', slug: 'reference/python-api' },
        { label: 'Graph format', slug: 'reference/graph-format' },
        { label: 'CLI and viewer', slug: 'reference/cli' },
      ] },
      { label: 'Explanation', items: [
        { label: 'Execution model', slug: 'explanation/execution-model' },
        { label: 'Workflow storage', slug: 'explanation/storage' },
      ] },
      { label: 'How-to guides', items: [
        { label: 'Run workflows in the REPL', slug: 'guides/live-workflows' },
        { label: 'Explore complex workflows', slug: 'guides/complex-examples' },
        { label: 'Generate skill-name types', slug: 'guides/skill-types' },
        { label: 'Troubleshoot a workflow', slug: 'guides/troubleshooting' },
      ] },
    ],
  })],
});
