export const workspaceViews = {
  builder: 'Builder',
  inspector: 'Inspector',
  debugger: 'Debugger',
  sessions: 'Sessions',
  chat: 'Chat',
  'run-log': 'Run Log',
  'run-timeline': 'Run Timeline',
} as const;

export type WorkspaceView = keyof typeof workspaceViews;

export type WorkspacePreset = 'debug' | 'replay';
