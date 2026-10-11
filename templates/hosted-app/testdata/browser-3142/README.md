# Built generated-app browser evidence (#3142)

Captured with Node v24.15.0, npm 11.11.0, Playwright Core 1.61.1 and Chromium 149.0.7827.55.
The run passed 59/59 browser checks and 17/17 deterministic tests for each release (1.0.0 and 1.0.1), with zero skipped.
The existing scaffold suite passed 5/5 with zero skipped; `go vet ./...` and `go build -o /tmp/builder-3142/pyry ./cmd/pyry` passed.
Missing npm/browser and unsupported Node were separately checked for nonzero exits and prerequisite diagnostics.

Reproduce from the repository using Node >=24.15.0 <25.0.0, npm and an installed compatible Chromium:

```sh
HOSTED_APP_BROWSER=/absolute/path/to/chromium \
  node templates/hosted-app/smoke.mjs /tmp/hosted-app-proof
```

[Commands/results](commands.json) retain actual argv, toolchain test output and exits; [browser checks](browser.json) retain executed/passed/skipped counts, request records, startup environment and measurements; [versions/artifact inventory](versions.json) record prerequisites.
The named viewports are 320×800 and 1280×800 CSS pixels. Full-page screenshots include content below the viewport. Minimum measured text contrast is 5.60:1; all coarse-pointer controls measure 44px. No page overflow or CSP violation was observed.

| Proof | Phone | Desktop |
| --- | --- | --- |
| Layout/defaults | [320](layout-320.png) | [1280](layout-1280.png) |
| Keyboard focus | [320](focus-320.png) | [1280](focus-1280.png) |
| Pending acknowledgment | [320](saving-320.png) | [1280](saving-1280.png) |
| Acknowledged save | [320](saved-320.png) | [1280](saved-1280.png) |
| HTTP write failure | [320](fail-320.png) | [1280](fail-1280.png) |
| Uncertain committed write | [320](uncertain-320.png) | [1280](uncertain-1280.png) |
| HTTP 200 is not acknowledgment | [320](wrong-status-320.png) | [1280](wrong-status-1280.png) |
| Service restart/fresh context | [320](restart-320.png) | [1280](restart-1280.png) |
| Built 1.0.1 replacement/fresh context | [320](replacement-320.png) | [1280](replacement-1280.png) |

Read feedback: [HTTP failure](read-fail.png), [invalid record](invalid-read.png).
Fixture/service/browser processes were closed and temporary generated releases/data removed; these committed artifacts remain reviewable. The fixture uses synthetic identities/data and exact contract CSP/nosniff. This is built-browser fixture proof, with publication, native and live-channel acceptance assigned to later tickets.
