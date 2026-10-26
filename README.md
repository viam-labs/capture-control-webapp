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

The port defaults to 8888 and can be configured via the `port` attribute in your component's config.

### Offline mode

Not supported in v1: the app always signals through `app.viam.com`, so the machine and browser need internet access.
