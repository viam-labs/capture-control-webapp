# robot-demo-capture-controller

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

## Develop

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
   https://robot-demo-capture-controller_devrel.viamapplications.com
   ```

**Note:** After re-uploading a new version, you may need to hard-refresh (Cmd+Shift+R / Ctrl+Shift+R) to avoid seeing a cached version.

## Local server

**Note:** Local server currently only works for single-machine apps.

To serve your app on the local network, add the module to a machine on app.viam.com:

1. Go to app.viam.com → your machine → Config
2. Add the module by name (`devrel:robot-demo-capture-controller`)
3. Add a component: type `generic`, model `devrel:robot-demo-capture-controller:webapp`
4. Save config

Your app will then be available at:

```
http://<machine-ip>:8888
```

The port defaults to 8888 and can be configured via the `port` attribute in your component's config.

### Offline mode (no internet)

To use the local server without internet, viam-server needs an HTTP signaling endpoint. Add this to your machine's network config on app.viam.com (Raw JSON):

```json
{
  "network": {
    "no_tls": true
  }
}
```

Then your frontend needs to detect local mode and use local signaling instead of cloud signaling:

```js
const isLocal = document.cookie.includes('is_local=true');
const signalingAddress = isLocal
    ? `http://${window.location.hostname}:8080`
    : 'https://app.viam.com';
```

**Note:** `no_tls` replaces the default HTTPS listener with an HTTP listener. This means traffic on the local network is unencrypted. This is acceptable for trusted networks (factory floor, home) but not recommended for public networks.
