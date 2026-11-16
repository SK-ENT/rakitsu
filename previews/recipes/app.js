const spotlights = document.querySelector('#spotlights');
const graph = document.querySelector('#graph');
const detail = document.querySelector('#detail');
const search = document.querySelector('#search');
const category = document.querySelector('#category');
const tagFilter = document.querySelector('#tag-filter');
const tags = {
  'ollama': ['Ollama', 'Ollama'],
  'pipeline': ['Pipeline', 'パイプライン'],
  'parallel': ['Parallel steps', '並列ステップ'],
  'filesystem': ['Filesystem tools', 'ファイル操作ツール'],
  'csv': ['CSV', 'CSV'],
  'tool-gate': ['Required tool calls', 'ツール呼び出し必須'],
  'python': ['Python', 'Python'],
  'cli': ['CLI tools', 'CLIツール'],
  'policy-check': ['Deterministic policy checks', '固定ルールによる判定'],
  'multi-agent': ['Multi-agent handoffs', 'エージェント間の引き継ぎ'],
  'rakitsu-http': ['Rakitsu HTTP API', 'Rakitsu HTTP API'],
  'laya': ['Laya', 'Laya'],
  'jev': ['Jev', 'Jev'],
  'openai': ['OpenAI', 'OpenAI'],
  'gemini': ['Gemini', 'Gemini'],
  'paper-study': ['Paper study', '論文輪読'],
  'runtime-question': ['Runtime question', '実行時の質問'],
  'robotics': ['Robotics', 'ロボット工学'],
  'typed-decisions': ['Typed decisions', '型付き意思決定'],
  'vue': ['Vue', 'Vue'],
  'codex': ['Codex', 'Codex'],
  'vision': ['Image input', '画像入力'],
  'project-memory': ['Project memory', 'プロジェクトメモリ'],
  'auto-recall': ['Auto recall', '自動検索'],
  'chrome': ['Chrome', 'Chrome'],
  'puppeteer': ['Puppeteer', 'Puppeteer']
};
const count = document.querySelector('#result-count');
const catalogTotal = document.querySelector('#catalog-total');
const languageToggle = document.querySelector('#language-toggle');
const svgNamespace = 'http://www.w3.org/2000/svg';

let catalog = [];
let selected;
let edges = [];
let graphLines;
let language = 'en';

const japanesePatterns = {
  'Draft, then review': '下書きとレビュー',
  'Extract, then reconcile': '抽出と照合',
  'Parallel analysis': '並行分析',
  'Read, reconcile, write': '読み込み・照合・保存',
  'Deterministic policy check': '固定ルールによる確認',
  'Typed decisions': '選択肢に基づく意思決定',
  'Generate, verify, repair, learn': '生成・検証・修正・学習'
};

function japanese(item) {
  return {
    ...item, ...window.RakitsuJa.recipes[item.id], ...item.ja,
    category: window.RakitsuJa.categories[item.category] || item.category,
    pattern: item.ja?.pattern || japanesePatterns[item.pattern] || item.pattern,
  };
}

function copy(item) {
  return language === 'ja' ? japanese(item) : item;
}

function text(key, ...args) {
  const value = language === 'ja' ? window.RakitsuJa?.ui?.[key] : null;
  return typeof value === 'function' ? value(...args) : (value || ({
    showing: `Showing ${args[0]} of ${args[1]} recipes`, recipes: `${args[0]} recipe${args[0] === 1 ? '' : 's'}`, noMatch: 'No matching recipes. Try “review”, “parallel”, or “support”.', clearSearch: 'Clear the search to explore all recipes.', couldNotLoad: 'Could not load the catalog. Open this preview through its local server.',
    switchJapanese: 'Switch to Japanese', placeholder: 'Search problems, recipes, patterns…', searchLabel: 'Search recipes', filterLabel: 'Filter by category', allCategories: 'All categories', graphLabel: 'Problem, recipe, and pattern graph', testedBuild: 'Rakitsu tested build', runFolder: 'Run from the extracted folder', download: 'Download ZIP', downloadProject: 'Download project ZIP', playthrough: 'Tested playthrough', exampleOutput: 'Example output'
  }[key] || key));
}

const staticLabels = { 'private-catalog-label': 'privateCatalog', 'tested-recipes-label': 'testedRecipes', 'hero-eyebrow': 'eyebrow', 'hero-title-main': 'titleMain', 'hero-title-sub': 'titleSub', 'hero-lede': 'lede', 'hero-note-label': 'noteLabel', 'hero-note-copy': 'noteCopy', 'routes-label': 'routes', 'legend-problem': 'problem', 'legend-recipe': 'recipe', 'legend-pattern': 'pattern', 'graph-hint': 'graphHint', 'footer-copy': 'privateFooter' };
const englishLabels = Object.fromEntries(Object.keys(staticLabels).map(id => [id, document.getElementById(id).textContent]));

function recordEvent(recipe, event) {
  fetch('/analytics/event', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ recipe, event }),
    credentials: 'omit',
    referrerPolicy: 'no-referrer',
    keepalive: true,
  }).catch(() => {});
}

function tagLabel(id) {
  return tags[id][language === 'ja' ? 1 : 0];
}

function renderSpotlights() {
  const featured = catalog.filter(item => item.spotlight);
  spotlights.hidden = !featured.length;
  spotlights.replaceChildren();
  if (!featured.length) return;
  const ja = language === 'ja';
  const links = element('div', 'spotlight-links');
  for (const item of featured) {
    const localized = copy(item);
    const card = element('article', 'spotlight-card');
    const link = element('a', 'spotlight-title', localized.title);
    link.href = `?recipe=${item.id}${ja ? "&lang=ja" : ""}#detail`;
    const report = element('a', 'spotlight-report', ja ? '実行レポート' : 'Run report');
    report.href = `recipes/${item.id}/example-output.md`;
    report.addEventListener('click', () => recordEvent(item.id, 'preview'));
    card.append(link, element('p', '', localized.spotlight), report);
    links.append(card);
  }
  spotlights.append(element('h2', '', ja ? '注目の実行結果' : 'Featured runs'), links);
}

function applyUi() {
  const ja = language === 'ja';
  document.documentElement.lang = ja ? 'ja' : 'en';
  for (const [id, key] of Object.entries(staticLabels)) {
    document.getElementById(id).textContent = ja ? text(key) : englishLabels[id];
  }
  document.querySelector('.search input').placeholder = text('placeholder');
  document.querySelector('.search input').setAttribute('aria-label', text('searchLabel'));
  category.setAttribute('aria-label', text('filterLabel'));
  graph.setAttribute('aria-label', text('graphLabel'));
  for (const option of category.options) option.textContent = option.value ? (language === 'ja' ? (window.RakitsuJa?.categories?.[option.value] || option.value) : option.value) : text('allCategories');
  tagFilter.setAttribute('aria-label', ja ? '技術・機能で絞り込む' : 'Filter by technology or feature');
  for (const option of tagFilter.options) {
    option.textContent = option.value ? tagLabel(option.value) : (ja ? 'すべての技術・機能' : 'All technologies and features');
  }
  languageToggle.textContent = ja ? 'English' : '日本語';
  languageToggle.lang = ja ? 'en' : 'ja';
  document.getElementById('analytics-link').textContent = ja ? '利用状況' : 'Analytics';
  renderSpotlights();
  languageToggle.setAttribute('aria-label', text(ja ? 'switchEnglish' : 'switchJapanese'));
}

function element(tag, className, text) {
  const node = document.createElement(tag);
  node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function makeNode(kind, label, note, column, row, onSelect) {
  const button = element('button', 'node ' + kind);
  button.type = 'button';
  button.style.gridColumn = column;
  button.style.gridRow = row;
  const kinds = language === 'ja' ? { problem: '問題', recipe: 'レシピ', pattern: 'パターン' } : {};
  button.append(element('span', 'node-type', kinds[kind] || kind), element('span', 'node-title', label));
  if (note) button.append(element('span', 'node-meta', note));
  button.addEventListener('click', () => {
    selected = onSelect();
    recordEvent(selected, 'view');
    updateSelection();
  });
  graph.append(button);
  return button;
}

function drawEdges() {
  if (!graphLines) return;
  const bounds = graph.getBoundingClientRect();
  graphLines.replaceChildren();
  for (const { from, to, id } of edges) {
    const a = from.getBoundingClientRect();
    const b = to.getBoundingClientRect();
    const x1 = a.right - bounds.left;
    const x2 = b.left - bounds.left;
    const y1 = a.top + a.height / 2 - bounds.top;
    const y2 = b.top + b.height / 2 - bounds.top;
    const mid = (x1 + x2) / 2;
    const path = document.createElementNS(svgNamespace, 'path');
    path.setAttribute('d', `M ${x1},${y1} C ${mid},${y1} ${mid},${y2} ${x2},${y2}`);
    if (id === selected) path.classList.add('active');
    graphLines.append(path);
  }
}

function render() {
  const terms = search.value.toLowerCase().trim().split(/\s+/);
  const items = catalog.filter(item => {
    if (category.value && item.category !== category.value) return false;
    if (tagFilter.value && !item.tags.includes(tagFilter.value)) return false;
    const labels = item.tags.flatMap(id => [id, ...tags[id]]);
    const localized = japanese(item);
    const words = [item.problem, item.title, item.category, item.description, item.pattern, item.flow, localized.problem, localized.title, localized.category, localized.description, localized.pattern, localized.flow, ...labels]
      .join(' ').toLowerCase().split(/[^\p{L}\p{N}]+/u);
    return terms.every(term => words.some(word => /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}]/u.test(term)
      ? word.includes(term) : word.startsWith(term)));
  });
  if (!items.some(item => item.id === selected)) selected = items[0]?.id;
  count.textContent = text('showing', items.length, catalog.length);
  graph.replaceChildren();
  edges = [];
  graphLines = document.createElementNS(svgNamespace, 'svg');
  graphLines.setAttribute('class', 'graph-lines');
  graphLines.setAttribute('aria-hidden', 'true');
  graph.append(graphLines);

  if (!items.length) graph.append(element('p', 'empty', text('noMatch')));

  const patterns = new Map();
  items.forEach((item, index) => {
    const localized = copy(item);
    if (!patterns.has(localized.pattern)) patterns.set(localized.pattern, []);
    patterns.get(localized.pattern).push({ item, row: index + 1 });
    const problem = makeNode('problem', localized.problem, '', 1, index + 1, () => item.id);
    const recipe = makeNode('recipe', localized.title, localized.category, 2, index + 1, () => item.id);
    problem.dataset.recipes = item.id;
    recipe.dataset.recipes = item.id;
    edges.push({ from: problem, to: recipe, id: item.id });
  });

  for (const [label, entries] of patterns) {
    const ids = entries.map(entry => entry.item.id);
    const pattern = makeNode('pattern', label, language === 'ja' ? text('recipes', ids.length) : `${ids.length} ${ids.length === 1 ? 'recipe' : 'recipes'}`, 3, entries[0].row, () => {
      const current = ids.indexOf(selected);
      return ids[(current + 1) % ids.length];
    });
    pattern.dataset.recipes = ids.join(' ');
    for (const id of ids) {
      const from = graph.querySelector(`.recipe[data-recipes="${CSS.escape(id)}"]`);
      edges.push({ from, to: pattern, id });
    }
  }

  updateSelection();
}

function workflowPreview(item) {
  const ja = language === 'ja';
  const section = element('section', 'workflow-preview');
  const root = `recipes/${item.id}`;
  const image = element('img', 'workflow-image');
  image.alt = ja ? 'レシピのワークフロー' : 'Recipe workflow';
  image.src = `${root}/workflow.svg`;

  const workflow = element('a', 'workflow-link');
  workflow.href = image.src;
  workflow.target = '_blank';
  workflow.rel = 'noopener';
  workflow.addEventListener('click', () => recordEvent(item.id, 'preview'));
  workflow.append(image);

  const settings = element('div', 'workflow-settings');
  const code = element('pre', 'config-source', ja ? '設定を読み込み中…' : 'Loading configuration…');
  code.tabIndex = 0;
  code.setAttribute('aria-label', ja ? '設定ファイルの内容' : 'Configuration source');
  settings.append(code);
  fetch(`${root}/settings-preview.json`).then(response => {
    if (!response.ok) throw new Error('Configuration unavailable');
    return response.json();
  }).then(files => {
    const picker = element('select', 'config-picker');
    picker.setAttribute('aria-label', ja ? '設定ファイル' : 'Configuration file');
    for (const [index, config] of files.entries()) {
      const option = element('option', '', config.file);
      option.value = index;
      picker.append(option);
    }
    code.textContent = files[0].source;
    if (files.length > 1) {
      picker.addEventListener('change', () => {
        code.textContent = files[picker.value].source;
        code.scrollTop = 0;
        recordEvent(item.id, 'preview');
      });
      settings.prepend(picker);
    } else {
      settings.prepend(element('span', 'config-name', files[0].file));
    }
  }).catch(() => {
    code.textContent = ja ? '設定を読み込めませんでした。' : 'Could not load the configuration.';
  });

  section.append(
    element('h3', '', ja ? 'ワークフロー' : 'Workflow'),
    workflow,
    element('h3', '', ja ? 'Rakitsu の設定' : 'Rakitsu configuration'),
    settings,
  );
  return section;
}

function updateSelection() {
  for (const node of graph.querySelectorAll('button')) {
    node.setAttribute('aria-pressed', String(node.dataset.recipes.split(' ').includes(selected)));
  }
  drawEdges();
  const item = catalog.find(recipe => recipe.id === selected);
  detail.replaceChildren();
  if (!item) {
    detail.append(element('p', 'empty', text('clearSearch')));
    return;
  }

  const localized = copy(item);
  const technical = element('div', 'detail-section');
  technical.append(
    element('div', 'detail-label', text('testedBuild')),
    element('div', 'version', item.version),
    element('p', 'requirement', localized.runtime || `${language === 'ja' ? 'ローカル Ollama' : 'Local Ollama'} · ${item.model}`),
    element('div', 'detail-label', text('runFolder')),
    element('pre', 'command', item.command),
  );
  if (item.toolAccess) {
    technical.append(element('p', 'requirement', localized.toolAccess));
  }
  const tagList = element('div', 'recipe-tags');
  tagList.setAttribute('role', 'group');
  tagList.setAttribute('aria-label', language === 'ja' ? '技術・機能タグ' : 'Technology and feature tags');
  for (const id of item.tags) {
    const chip = element('button', 'recipe-tag', tagLabel(id));
    chip.type = 'button';
    chip.setAttribute('aria-pressed', String(tagFilter.value === id));
    chip.addEventListener('click', () => {
      tagFilter.value = tagFilter.value === id ? '' : id;
      render();
      tagFilter.focus();
    });
    tagList.append(chip);
  }
  const actions = element('div', 'actions');
  const download = element('a', 'button primary', item.projectFiles ? text('downloadProject') : text('download'));
  download.href = `downloads/${item.id}.zip`;
  download.download = '';
  const output = element('a', 'button secondary', item.id === 'night-shift' ? text('playthrough') : (item.spotlight ? (language === 'ja' ? '実行レポート' : 'Run report') : text('exampleOutput')));
  output.href = `recipes/${item.id}/example-output.md`;
  download.addEventListener('click', () => recordEvent(item.id, 'download'));
  output.addEventListener('click', () => recordEvent(item.id, 'preview'));
  actions.append(download, output);
  detail.append(
    element('span', 'detail-kicker', localized.category),
    element('h2', '', localized.title),
    element('p', 'detail-copy', localized.description),
    element('span', 'route-chip', localized.flow),
    tagList,
    technical,
    workflowPreview(item),
    actions,
  );
}

search.addEventListener('input', render);
category.addEventListener('change', render);
tagFilter.addEventListener('change', render);
languageToggle.addEventListener('click', () => { language = language === 'en' ? 'ja' : 'en'; applyUi(); render(); });
document.addEventListener('keydown', event => {
  if (event.key === '/' && event.target === document.body) {
    event.preventDefault();
    search.focus();
  }
});
new ResizeObserver(drawEdges).observe(graph);

async function init() {
  const response = await fetch('catalog.json');
  if (!response.ok) throw new Error('Catalog unavailable');
  catalog = await response.json();
  catalogTotal.textContent = catalog.length;
  const requested = new URLSearchParams(location.search).get('recipe');
  if (catalog.some(item => item.id === requested)) {
    selected = requested;
    recordEvent(selected, 'view');
  }
  if (new URLSearchParams(location.search).get('lang') === 'ja') language = 'ja';
  for (const value of [...new Set(catalog.map(item => item.category))].sort()) {
    const option = document.createElement('option');
    option.value = value;
    option.textContent = language === 'ja' ? (window.RakitsuJa?.categories?.[value] || value) : value;
    category.append(option);
  }
  const usedTags = new Set(catalog.flatMap(item => item.tags));
  for (const id of [...usedTags].sort((a, b) => tags[a][0].localeCompare(tags[b][0]))) {
    const option = document.createElement('option');
    option.value = id;
    tagFilter.append(option);
  }
  applyUi();
  render();
}

init().catch(() => {
  count.textContent = '';
  graph.replaceChildren(element('p', 'empty', text('couldNotLoad')));
});
