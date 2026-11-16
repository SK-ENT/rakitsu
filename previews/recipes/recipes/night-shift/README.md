# Night Shift

Play the mechanic in a blackout repair workshop. Walk to Mara for a work
order and ask Ivo about the spare battery. Restore the clinic radio and
shelter heater with hand repairs or install the battery in the assigned job.

## Setup

- Rakitsu `v0.3.0-alpha.1-internal.1897` was tested (source commit `b7ea2bfc`).
  Use an internal build with the Laya Decision pipeline feature.
- Laya 0.4.31 was tested with its English checkpoint on CPU. Start Laya on
  loopback port 8000 with the checkpoint loaded. The project includes
  Decision settings. Install the Rakitsu binary and Laya model separately.
- Install Node.js 22.12 or newer.

Start Laya using its [local setup instructions](https://github.com/NandhaKishorM/laya).
Then start a dedicated Rakitsu server in this folder:

```sh
rakitsu version
rakitsu serve --host 127.0.0.1 --port 19367
```

In another terminal in the same folder:

```sh
npm ci
npm run dev
```

Open http://127.0.0.1:19368/. Use WASD or arrow keys to walk and E to
interact. On-screen arrows and the interaction button work with touch.

## How the pieces fit

The local Vite server forwards the game's HTTP requests to Rakitsu on
port 19367. Approaching Mara or Ivo and interacting runs one Decision
step with local Laya. See `decision.json` and `nightShift.ts` for the workflow
settings and legal actions. The browser applies the validated choices to
the workshop, and handles movement, inventory and repairs. The config
defines zero chat agents and needs no chat-model API key.
