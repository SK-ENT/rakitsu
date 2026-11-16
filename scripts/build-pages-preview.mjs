// Invoked by build-pages.sh for the 0.4 preview and published alpha docs.
import { cpSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { posix } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = fileURLToPath(new URL('../', import.meta.url));
const output = process.argv[2];
const theme = 'v0.4.0';
const releaseTag = process.argv[3];
const tag = releaseTag || theme;
const draft = releaseTag ? posix.join(output, tag) : posix.join(output, 'preview', tag);
const assets = releaseTag ? '../assets/' : '../../assets/';
const source = posix.join(repo, 'site/preview');
const read = (name) => readFileSync(posix.join(repo, name), 'utf8');
const config = JSON.parse(read('site/preview/' + theme + '.json'));
const baseline = JSON.parse(read('site/data/' + config.baseline + '.json'));

let MarkdownIt;
try {
  ({ default: MarkdownIt } = await import('../web/node_modules/markdown-it/index.mjs'));
} catch (error) {
  if (error.code !== 'ERR_MODULE_NOT_FOUND') throw error;
  throw new Error('The preview needs the frontend dependencies. Run npm --prefix web ci first.', { cause: error });
}

for (const name of ['brand', 'diagrams', 'showcase']) {
  cpSync(posix.join(repo, 'docs/assets', name), posix.join(draft, 'docs/assets', name), { recursive: true });
}
cpSync(posix.join(source, theme + '-indigo.css'), posix.join(draft, theme + '-indigo.css'));
const data = {
  baseline: config.baseline,
  meta: { ...baseline.meta, status: null },
  previewNotice: config.previewNotice,
  story: config.story,
  features: [...baseline.features.map(feature => feature.id === 'hub' ? {
    ...feature, simple: config.hub, detail: [...config.hubDetail, ...feature.detail],
  } : feature), ...config.features],
};
if (!releaseTag) writeFileSync(posix.join(draft, 'reference.json'), JSON.stringify(data, null, 2) + '\n');

const index = posix.join(draft, 'index.html');
const html = (releaseTag ? readFileSync(index, 'utf8') : read('site/index.html')
  .replace('<title>Rakitsu Spec Reference</title>', '<title>Rakitsu ' + tag + ' documentation preview</title>')
  .replaceAll(/(href|src)="assets\//g, '$1="../../assets/')
  .replace('<script src="../../assets/viz.js">', '<script>window.RK_TAG = window.RK_PREVIEW = "v0.4.0";</script>\n<script src="../../assets/viz.js">'))
  .replace('<html lang="en">', '<html lang="en" data-preview="' + theme + '">')
  .replace('</head>', '<link rel="stylesheet" href="v0.4.0-indigo.css">\n</head>');
writeFileSync(index, html);

const docs = new Map([
  ['README.md', 'README.html'], ['README.ja.md', 'README.ja.html'],
  ['docs/WEBUI.md', 'docs/WEBUI.html'], ['docs/WEBUI.ja.md', 'docs/WEBUI.ja.html'],
]);
const md = new MarkdownIt({ html: true, linkify: true });
md.core.ruler.push('preview_links', state => {
  const { current, lang } = state.env;
  const seen = new Map();
  function visit(tokens) {
    for (const [i, token] of tokens.entries()) {
      if (token.type === 'heading_open') {
        const name = tokens[i + 1].content.toLowerCase()
          .replace(/[^\p{L}\p{N}_ -]/gu, '').replaceAll(' ', '-');
        const count = seen.get(name) || 0;
        seen.set(name, count + 1);
        token.attrSet('id', count ? name + '-' + count : name);
      }
      if (token.type === 'link_open') {
        const href = token.attrGet('href');
        if (href && !/^(?:[#/]|[a-z][\w+.-]*:)/i.test(href)) {
          const [name, fragment] = href.split('#');
          const target = posix.normalize(posix.join(posix.dirname(current), name));
          const hash = fragment ? '#' + fragment : '';
          if (docs.has(target)) {
            token.attrSet('href', posix.relative(posix.dirname(current), docs.get(target)) + hash);
          } else if (target === 'docs/reference/agents-orchestrators.md' && fragment === 'decision-and-retrieval-steps') {
            token.attrSet('href', '../#' + lang + '.ref-decision-retrieval');
          } else if (target === 'docs/reference/index.md') {
            token.attrSet('href', './#' + lang + '.ref-hub');
          } else if (!target.startsWith('docs/assets/')) {
            token.attrSet('href', 'https://github.com/SK-ENT/rakitsu/blob/' + (releaseTag || config.baseline) + '/' + target + hash);
          }
        }
      }
      if (token.children) visit(token.children);
    }
  }
  visit(state.tokens);
});
for (const [current, file] of docs) {
  const ja = current.endsWith('.ja.md');
  const lang = ja ? 'ja' : 'en';
  const guide = current.startsWith('docs/');
  const prefix = guide ? '../' : '';
  const title = guide ? (ja ? 'Web UI マニュアル' : 'Web UI manual') : 'README';
  const peer = posix.basename(docs.get(ja ? current.replace('.ja.md', '.md') : current.replace('.md', '.ja.md')));
  mkdirSync(posix.dirname(posix.join(draft, file)), { recursive: true });
  writeFileSync(posix.join(draft, file), `<!doctype html>
<html lang="${lang}" data-preview="${theme}" class="preview-manual">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Rakitsu ${tag} · ${title}</title>
<link rel="stylesheet" href="${prefix}${assets}app.css">
<link rel="stylesheet" href="${prefix}v0.4.0-indigo.css">
</head>
<body><div class="wrap"><main>
<nav class="preview-doc-nav" aria-label="${ja ? 'ドキュメント' : 'Documentation'}">
  <a href="${prefix}./#${lang}.ref-web-ui-story">${ja ? 'リファレンス' : 'Reference'}</a>
  <a href="${peer}" hreflang="${ja ? 'en' : 'ja'}" lang="${ja ? 'en' : 'ja'}">${ja ? 'English' : '日本語'}</a>
</nav>
${md.render(read(current), { current, lang })}
</main></div></body></html>\n`);
}
