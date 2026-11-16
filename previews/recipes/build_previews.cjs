// Run with the path to the checkout's installed js-yaml package.
const { readFileSync, writeFileSync } = require('node:fs');
const { join, resolve } = require('node:path');

const yaml = require(resolve(process.argv[2]));
const recipes = JSON.parse(readFileSync('catalog.json', 'utf8'));
const projects = {
  "runtime-question-decision": {
    files: ["config.json"],
    stages: [["Question and input at runtime"], ["Rakitsu Decision · chosen backend"], ["Fixed yes/no answer"]],
  },
  "robot-cell-recovery-decisions": {
    files: ["stages.json", "laya.json", "jev.json", "openai.json", "gemini.json"],
    stages: [["Operator, maintenance and inspection notes"], ["Laya · incident category"],
      ["Jev · maintenance recurrence"], ["OpenAI · quality quarantine"], ["Gemini · team ownership"],
      ["Fixed interlock rule → human review packet"]],
  },
  'night-shift': {
    files: ['decision.json'],
    stages: [['Player talks to Mara or Ivo'], ['Rakitsu · Laya Decision'], ['Game applies the choice']],
  },
  'image-to-ui-verified-lessons': {
    files: ['harness/learn.yaml', 'harness/generate.yaml', 'harness/repair.yaml', 'followup/learn.yaml'],
    stages: [['Learn shared components'], ['Generate from image and spec'], ['Check in Chrome'],
      ['Repair and recheck'], ['Save and retrieve the lesson']],
  },
};

const escape = value => String(value).replace(/[&<>"']/g, char => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&apos;',
}[char]));

function stepLabel(step) {
  return [step.agent || step.name, [
    step.decision?.backend ? `Decision · ${step.decision.backend}` : step.name !== step.agent ? step.name : '',
    step.require_tool_call?.tool || '',
  ].filter(Boolean)];
}

function workflowSvg(stages, title) {
  const width = 360;
  const row = 66;
  const height = stages.length * row - 10;
  let markup = '';
  for (const [index, stage] of stages.entries()) {
    const y = index * row + 4;
    const segment = width / stage.length;
    if (index > 0) {
      const previous = stages[index - 1];
      for (let i = 0; i < previous.length; i++) {
        const x1 = (i + 0.5) * width / previous.length;
        for (let j = 0; j < stage.length; j++) {
          const x2 = (j + 0.5) * segment;
          markup += `<path d="M${x1} ${y - 18} V${y - 9} H${x2} V${y}" fill="none" stroke="#82a896" stroke-width="1.6"/>`;
        }
      }
    }
    for (const [i, [label, notes = []]] of stage.entries()) {
      const x = i * segment + 8;
      markup += `<rect x="${x}" y="${y}" width="${segment - 16}" height="48" rx="8" fill="#f5faf7" stroke="#b6cfbf"/>`;
      markup += `<text x="${x + 12}" y="${y + (notes.length ? 20 : 29)}" fill="#163d2d" font-size="13" font-weight="700">${escape(label)}</text>`;
      if (notes.length) markup += `<text x="${x + 12}" y="${y + 36}" fill="#486559" font-size="10">${escape(notes.join(' · '))}</text>`;
    }
  }
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${width} ${height}" font-family="system-ui, sans-serif"><title>${escape(title)}</title>${markup}</svg>\n`;
}

for (const recipe of recipes) {
  const root = join('recipes', recipe.id);
  const project = projects[recipe.id];
  const files = project?.files || ['config.yaml'];
  const configs = files.map(file => ({ file, source: readFileSync(join(root, file), 'utf8') }));
  const steps = project ? null : yaml.load(configs[0].source).orchestrator.pipeline.steps;
  const stages = project?.stages.map(stage => stage.map(label => [label])) ||
    steps.map(step => step.type === 'parallel' ? step.steps.map(stepLabel) : [stepLabel(step)]);
  writeFileSync(join(root, 'workflow.svg'), workflowSvg(stages, recipe.title));
  writeFileSync(join(root, 'settings-preview.json'), JSON.stringify(configs) + '\n');
}
