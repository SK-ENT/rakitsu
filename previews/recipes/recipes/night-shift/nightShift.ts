import decisionConfig from './decision.json';

export const jobs = { clinic: 'Clinic radio', shelter: 'Shelter heater' } as const;
export type Job = keyof typeof jobs;
export type Role = 'Dispatcher' | 'Quartermaster';
type Battery = 'stock' | 'spent' | Job;
export type Workshop = { battery: Battery; progress: Record<Job, number>; priority: Job };

export function newWorkshop(): Workshop {
  return { battery: 'stock', progress: { clinic: 0, shelter: 0 }, priority: 'clinic' };
}

type Decision = { instructions: string; choices: Record<string, string>; apply: (choice: string) => string };

function roleDecision(role: Role, state: Workshop): Decision {
  const unfinished = (Object.keys(jobs) as Job[]).filter(job => state.progress[job] < 3);
  if (role === 'Dispatcher') {
    if (!unfinished.length) return { instructions: 'Both jobs are complete.', choices: { monitor: 'Keep the dispatch board open for follow-up.', close_board: 'Close the dispatch board.' }, apply: () => 'Both requests are complete. No new work is assigned.' };
    return {
      instructions: 'Choose the priority for this turn. Respect the player request unless work is already complete or another job is more urgent. The clinic radio supports emergency dispatch and the shelter heater keeps evacuees warm.',
      choices: { hold: 'Keep the current dispatch priority if unfinished, otherwise forward the remaining job.', ...Object.fromEntries(unfinished.map(job => [job, `Prioritize ${jobs[job]} for the quartermaster and mechanic.`])) },
      apply(choice) {
        if (choice !== 'hold') state.priority = choice as Job;
        else if (!unfinished.includes(state.priority)) state.priority = unfinished[0]!;
        return `The work order now prioritizes the ${jobs[state.priority]}.`;
      },
    };
  }
  const priority = state.priority;
  const battery = state.battery;
  if (battery !== 'stock' || state.progress[priority] > 1) {
    return {
      instructions: 'Account for inventory.',
      choices: { report: 'Send an inventory status report.', audit: 'Audit stock and log the battery status.' },
      apply() {
        if (battery === 'spent') return 'The spare has been installed. No additional battery is available.';
        if (battery !== 'stock') return `The spare is already assigned to the ${jobs[battery]}.`;
        return 'The dispatch priority needs at most one repair step. The spare stays in stock.';
      },
    };
  }
  return {
    instructions: 'Release the spare to the dispatcher’s priority if it still needs at least two repair steps.',
    choices: {
      reserve: 'Keep the battery in stock and leave both jobs to manual work this turn.',
      [priority]: `Issue the only battery to the ${jobs[priority]}, making a one-turn repair available.`,
    },
    apply(choice) {
      if (choice !== priority) return 'The spare stays in stock. The mechanic can make one manual repair step.';
      state.battery = priority;
      return `The mechanic can now install the battery in the ${jobs[priority]}.`;
    },
  };
}

async function choose(base: string, role: Role, query: string, decision: Decision): Promise<string> {
  async function request(path: string, body?: object) {
    const response = await fetch(base + path, body ? {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    } : undefined);
    const data = await response.json();
    if (!response.ok || data.error) throw new Error(data.error || 'Rakitsu request failed.');
    return data;
  }
  const yaml = JSON.stringify({
    name: `Night shift · ${role}`, agents: [],
    orchestrator: { name: 'workshop', strategy: 'Pipeline', pipeline: { steps: [{
      name: role.toLowerCase(), type: 'decision', decision: { backend: decisionConfig.backend, questions: {
        action: { type: decisionConfig.questionType, instructions: decision.instructions, choices: decision.choices },
      } },
    }] } },
  });
  const config = await request('/api/configs/inline', { yaml });
  const run = await request('/api/run', { config_id: config.id, query, timeout: 35 });
  for (let attempt = 0; attempt < 160; attempt++) {
    const status = await request('/api/run');
    if (status.session_id !== run.session_id) throw new Error('Another Rakitsu run replaced the NPC decision.');
    if (status.status === 'completed') {
      const result = JSON.parse(status.result);
      const choice = result.answers?.action?.choice;
      if (result.backend !== 'laya' || typeof choice !== 'string' || !Object.prototype.hasOwnProperty.call(decision.choices, choice)) {
        throw new Error('Laya did not return a legal action.');
      }
      return choice;
    }
    if (status.status !== 'running') throw new Error('The NPC decision stopped before returning a choice.');
    await new Promise(resolve => setTimeout(resolve, 150));
  }
  throw new Error('The NPC decision timed out.');
}

export async function talk(base: string, role: Role, workshop: Workshop, requested: Job) {
  const state = { ...workshop, progress: { ...workshop.progress } };
  const decision = roleDecision(role, state);
  const query = [
    `Mechanic requests: ${jobs[requested]}.`,
    `Dispatch priority: ${jobs[state.priority]}.`,
    `Clinic radio: ${state.progress.clinic}/3. Shelter heater: ${state.progress.shelter}/3.`,
    `Battery: ${state.battery}.`,
  ].join('\n');
  const action = await choose(base, role, query, decision);
  const consequence = decision.apply(action);
  return { state, consequence };
}
