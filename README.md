# capture-control-webapp

A Viam app module that serves your web app from your machine via the Viam SDK.

For full documentation, see [Viam Apps](https://docs.viam.com/build-apps/hosting/overview/).

## Machine setup

1. Add the capture controller sensor (module `viam:capture-control`) and list the resources to record:

```json
{
  "name": "demo-capture",
  "api": "rdk:component:sensor",
  "model": "viam:capture-control:capture-control-sensor",
  "attributes": {
    "resources": [
      { "resource_name": "arm", "method": "JointPositions" },
      { "resource_name": "wrist-cam", "method": "GetImages" }
    ]
  }
}
```

2. Point the data manager at it, and add the sensor to the data manager's `depends_on`:

```json
"attributes": { "capture_control_sensor": { "name": "demo-capture", "key": "overrides" } }
```

Each episode becomes one Viam sequence tagged `task:<task>`, `session:<id>`, `fps:<fps>`. Nothing is captured between episodes.

3. Optional, for teleop and home resets:
   - A `devrel:so101:teleop` generic service. The app uses it automatically if there's exactly one. Keep `manage_leader_torque` at its default (`true`).
   - One `erh:vmodutils:arm-position-saver` switch per arm, for example `leader-home` and `follower-home`. Choose the home set under **Robot setup**.

**H** stops teleop and moves each arm in the home set to its saved pose. Teleop stays off afterwards; press **T** once your hand is on the leader. While teleop is off, a reset doesn't start the next episode on its own; it waits for **Space**.

### Keyboard

| Key | Action |
|---|---|
| Space | End episode / start next episode |
| Esc | Stop session |
| 1–9 | Pick task for the next episode |
| T | Toggle teleop (while recording: stop teleop and end the episode) |
| H | Home arms (not while recording) |
| Enter | Start session |
| ? | Shortcut overlay |

Tasks are saved in the browser. Each episode is tagged `task:<the task active when it started>`.

### Reviewing episodes

When a session ends, **Review session** lists that session's episodes as they sync. Each episode plays its camera frames in real time. **K** tags the episode `review:keep` and **D** tags it `review:discard`. Each one then jumps to the next episode you haven't reviewed. Nothing is deleted; untagged episodes count as not reviewed.

**Episodes** opens the same player for this machine's past episodes, with filters for task, session and review status.

| Key | Action |
|---|---|
| K / D | Keep / discard, then go to the next unreviewed episode |
| ← / → | Previous / next episode |
| Space | Play / pause |
| , / . | Step one frame |
| Esc | Close review |

Review reads and tags data with the machine's own credentials. A sequence can appear before all its frames have synced. Keep and Discard unlock once the frames have caught up, or once no new frames arrive for 15 s, in which case the episode is marked partial.

## Develop

Requires Go and Node 22.18+ (tests use Node's built-in TypeScript support).

```bash
make setup     # go deps + npm ci
npm run dev    # Vite dev server
viam module local-app-testing --app-url http://localhost:5173 --machine-id <machine-id>  # injects credential cookies; open http://localhost:8012/start
npm test       # session engine tests
make           # go test + npm test + module.tar.gz
```

## Upload to viamapplications.com

1. Build the module:
   ```
   make setup
   make
   ```

2. Upload `module.tar.gz` (not the binary) to the registry (`--version` is required and must be a valid semver). Use the platform matching your build machine:
   ```
   viam module upload --upload=./module.tar.gz --platform=linux/amd64 --version=0.1.0  # Linux x86
   viam module upload --upload=./module.tar.gz --platform=darwin/arm64 --version=0.1.0  # Apple Silicon
   ```

3. Access your app at:
   ```
   https://capture-control-webapp_viam-labs.viamapplications.com
   ```

**Note:** After re-uploading a new version, you may need to hard-refresh (Cmd+Shift+R / Ctrl+Shift+R) to avoid seeing a cached version.

## Local server

**Note:** Local server currently only works for single-machine apps.

To serve your app on the local network, add the module to a machine on app.viam.com:

1. Go to app.viam.com → your machine → Config
2. Add the module by name (`viam-labs:capture-control-webapp`)
3. Add a service: type `generic`, model `viam-labs:capture-control:webapp`
4. Save config

Your app will then be available at:

```
http://<machine-ip>:8888
```

The port defaults to 8888 and can be configured via the `port` attribute in the service's config.

### Offline mode

Not supported in v1: the app always signals through `app.viam.com`, so the machine and browser need internet access.
