// Authored Markdown links follow the deployment base, like Starlight navigation.
export default function baseLinks(base = '/') {
  const prefix = base.replace(/\/$/, '');
  function rewrite(node, context) {
    if (node.url.startsWith('/') && !node.url.startsWith('//') && prefix &&
        node.url !== prefix && !node.url.startsWith(`${prefix}/`)) {
      context.setProperty(node, 'url', prefix + node.url);
    }
  }
  return { name: 'deployment-base-links', link: rewrite, image: rewrite, definition: rewrite };
}
