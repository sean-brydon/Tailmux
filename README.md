# Tailmux

A Go CLI and interactive dashboard for development machines across separate Tailscale accounts: persistent terminals, saved port forwards, public preview URLs and remote agent runtimes.

Profile names and hostnames are supplied at runtime. Each profile gets its own embedded Tailscale (`tsnet`) node and private state directory. Your system Tailscale connection stays independent.

## Dashboard

```bash
tailmux                  # interactive control centre
tailmux status --json    # scriptable, read-only snapshot
```

The default **0 Monitor** panel shows RAM meters, Orca/Herdr session status, reported unread updates and available Codex account quotas and opt-in Claude telemetry for saved boxes. Limits belong to the default CLI account and may be shared across boxes; unsupported attention and quota data are marked unavailable. Use `tailmux monitor --json` for a one-shot report.

A Charm-powered dashboard brings boxes, forwards, Orca routes and setup into one terminal. Press Enter to open a box in tmux/Zellij, `p` to select a remote port, `f` to configure a saved/public forward, Shift+L to review isolated loopback setup, `c` to check host setup, or `a` to add an account. `n` starts networking and saved forwards; quitting leaves existing sessions running. See [dashboard controls](docs/content/docs/dashboard.mdx).

Bare `tailmux` prints help when input/output is redirected. `tailmux dashboard` requires an interactive terminal.

## Build

Requires macOS or Linux, Go 1.26.6+, and OpenSSH. Herdr must be installed on remote hosts for Herdr session commands. `terminal` needs fzf and tmux or Zellij locally, plus tmux remotely. Forwarding needs SSH local forwarding permission; no remote Tailmux installation is required.

```sh
go build -o bin/tailmux ./cmd/tailmux
./bin/tailmux login personal
./bin/tailmux login work
./bin/tailmux hosts
./bin/tailmux hosts add personal/devbox --user developer
./bin/tailmux hosts add work/buildbox --user developer
./bin/tailmux doctor --all
./bin/tailmux ssh personal/devbox
./bin/tailmux herdr attach personal/devbox
./bin/tailmux herdr attach work/buildbox
```

`personal`, `work`, `devbox`, and `buildbox` above are examples. Choose any profile labels and use the hostnames shown by `hosts`. `login` creates configuration on first use; `init` is optional and creates an empty config. Host registration is optional for direct commands such as `attach personal/devbox`; save hosts to set SSH overrides and include them in `doctor --all` and `sessions --all`.

Login prints an authentication link. Sign into the indicated account and check the reported tailnet before continuing. Each Tailmux node must be allowed to reach its host's SSH port by that tailnet's policy. For work, enroll the node according to your organization's device approval requirements.

Use a built binary at a stable path: the background process and SSH proxy invoke that binary. `go run` uses a temporary executable and is unsuitable for persistent operation. After rebuilding, run `tailmux stop` so the next command starts the updated daemon.

## Configuration

`init` prints the location of `config.json` and refuses to overwrite existing configuration. The default directory is the OS user config directory plus `tailmux` (on macOS, `~/Library/Application Support/tailmux`; on Linux, usually `~/.config/tailmux`). Set `TAILMUX_HOME` to an absolute private directory to override it.

```json
{
  "terminal_backend": "zellij",
  "profiles": ["personal", "work"],
  "hosts": {
    "personal/devbox": {"profile": "personal", "address": "devbox", "user": "developer", "port": 22},
    "work/buildbox": {"profile": "work", "address": "buildbox", "user": "developer", "port": 22}
  }
}
```

Use `tailmux hosts add <profile>/<hostname> --user <user> --port <port>` to save or update SSH settings. `--address` can override the dial destination. You can also edit the JSON directly. Otherwise OpenSSH selects the user from your SSH config or local username. Addresses can be MagicDNS names, full tailnet DNS names, or IP addresses. Host changes take effect on the next request.

## Commands

| Command | Behavior |
| --- | --- |
| `init` | Create an empty configuration |
| `hosts` | Discover visible peers across configured profiles |
| `hosts add <profile/host> [--user USER] [--address DNS] [--port PORT]` | Save per-host SSH settings |
| `login <profile>` | Create/reuse any named profile, authenticate, and display its tailnet |
| `doctor [host\|--all]` | Verify TCP connectivity to SSH on saved hosts; does not authenticate SSH |
| `ssh <host> [command...]` | Open a shell or execute a command using normal SSH authentication |
| `herdr attach <host> [session]` | Run remote `herdr session attach`, defaulting to `agents` |
| `herdr sessions <host\|--all> [--json]` | List Herdr sessions as a table; `--json` returns structured data |
| `herdr open [hosts...]` | One local Herdr view across saved or selected hosts |
| `orca serve <host>` | Start a preconfigured remote Orca user service and connect |
| `orca connect <host> [--ready-file PATH]` | Pair a running Orca runtime over SSH |
| `orca status <host>` | Restore its tunnel and verify the runtime |
| `orca exec <host> -- <command...>` | Run native Orca CLI commands on that runtime |
| `terminal [--backend tmux\|zellij] [host]` | Open the terminal box picker |
| `terminal --default tmux\|zellij` | Save backend preference without launching |
| `dashboard` / bare `tailmux` | Open the interactive control centre |
| `status [--json]` | Inspect boxes, forwards, runtime routes and tools |
| `ports <host> [--json\|--pick\|--forward]` | Discover/select remote TCP ports and processes |
| `setup check <host\|--all> [--json]` | Check remote prerequisites |
| `setup install <host> --tmux --ports` | Explicitly install selected prerequisites |
| `setup orca <host> [--apply]` | Review/configure an Orca user service |
| `loopback setup <host> [--name NAME]` | Assign a private hostname and isolated loopback address |
| `loopback list` | List configured box hostnames and addresses |
| `forward <host> <ports...> [--name NAME] [--no-rewrite] [--json]` | Forward TCP or serve a named HTTP route |
| `forwards [--json]` | List forwarding groups and status |
| `unforward <id>` | Stop one forwarding group |
| `help [command]` | Show usage; also `<command> --help` |
| `version` / `--version` | Print the binary version |
| `stop` | Stop the local daemon and disconnect active local transports |
| `daemon` | Run the local daemon in the foreground for diagnostics |
| `proxy <host>` | Internal SSH transport; stdout contains only connection bytes |

Remote command arguments are passed literally. For shell syntax, explicitly invoke a remote shell:

```sh
tailmux ssh personal/devbox sh -lc 'cd ~/project && git status'
```

`herdr attach` runs Herdr through an SSH terminal. Detach using Herdr's `Ctrl+B`, then `Q`; agents stay on the remote host. This initial attachment mode does not provide local Herdr clipboard bridging. Remote Herdr must be on the SSH command PATH. Tailmux does not install remote software or start agents automatically.

## Architecture

```text
Tailmux CLI -> private local Unix socket -> Tailmux daemon
                                          ├─ profile A tsnet -> host A:22
                                          └─ profile B tsnet -> host B:22
```

The daemon starts on demand and owns the tsnet nodes so concurrent terminals can share one profile safely. A process lock prevents duplicate daemon ownership. The SSH ProxyCommand asks the daemon to dial the configured host through the selected profile; OpenSSH handles user authentication, host verification, and encryption.

The profiles do not forward traffic between tailnets. Ambient Tailscale auth-key and OAuth environment variables are ignored; login is explicit per profile. State and the local socket live in a private directory outside the repository. SSH host-key aliases are scoped to profile and destination, and connection multiplexing is disabled so an existing SSH connection cannot bypass profile selection. OpenSSH uses `StrictHostKeyChecking=accept-new`: first-seen host keys are saved under the profile-specific alias, and changed keys are rejected. This is trust on first use; it does not independently verify a new host key.

Stopping the daemon closes local SSH connections and stops forwards. Groups created with `--save NAME` restore when the daemon next starts; temporary groups do not. Remote Herdr sessions continue, but ordinary nonpersistent SSH commands may terminate when disconnected.

## Testing

```sh
go test -race ./...
go vet ./...
GOOS=linux GOARCH=amd64 go build -o bin/tailmux-linux-amd64 ./cmd/tailmux
```

Tests cover invalid configuration, preserving existing config, shell quoting, account-specific SSH identity, protocol framing, and request cancellation. Real tailnet connectivity, SSH authentication, and session persistence require live validation:

1. Log in to both profiles and verify the reported tailnet names.
2. Run `doctor --all`.
3. Run `ssh <host> hostname` on each machine.
4. Attach to Herdr, start a harmless long-running command, detach, and reattach.
5. Keep sessions on both hosts open at once; verify both survive switching views.

The current version provides connectivity and per-host session attachment. Combined agent status, task dispatch, worktree provisioning, and cross-host handoffs are future work.

## References

- [Tailscale tsnet](https://pkg.go.dev/tailscale.com/tsnet)
- [Herdr persistence and remote access](https://herdr.dev/docs/persistence-remote/)

## One Herdr view for multiple hosts

```sh
tailmux herdr open
tailmux herdr open personal/devbox work/buildbox
tailmux herdr open personal/devbox
```

`herdr open` starts or reuses a dedicated **local** Herdr session named `tailmux`, adds one workspace per selected host, and attaches its TUI. With no arguments it uses saved hosts. Existing workspaces are retained, and the first selected host is focused. Reopening reuses active connections; idle local shells are reconnected. It leaves your default Herdr session alone.

Each workspace connects through Tailmux to the host's persistent remote Herdr session named `agents`. Herdr must be installed locally and on each selected host. The connection launcher offers Enter to reconnect after SSH exits, or `q` to return to the local shell. You can run `herdr open` again to reconnect an idle workspace.

Default keys with nested Herdr sessions:

- **Ctrl+B, W**, then **Up/Down** and **Enter**: select a host workspace locally.
- **Ctrl+B, Q**: detach the whole local view; its connections and remote sessions remain running.
- **Ctrl+B, Ctrl+B, Q**: pass the prefix through and detach only the current remote session.

These are nested terminal interfaces. The outer sidebar lists hosts; the inner sidebar lists that host's remote workspaces and agents. A combined cross-host agent list is not implemented yet. Custom Herdr keybindings may differ from the defaults above.


## Documentation site

The Fumadocs site lives in [`docs/`](docs/). It includes setup, account profiles,
SSH/1Password, Herdr sessions, the combined host view, and CLI reference pages.

```sh
cd docs
npm ci
npm run dev
```

Preview at `http://127.0.0.1:4310`. `npm run build` exports the static site to
`docs/out/`; `npm run typecheck` checks the TypeScript sources. The manual
**Publish docs** workflow deploys to GitHub Pages after Pages is enabled for
GitHub Actions. For a repository subpath, it passes `DOCS_BASE_PATH` during build.

## Release downloads

`install.sh` installs macOS/Linux binaries from GitHub Releases, verifies the
archive checksum, and atomically replaces the executable in `~/.local/bin`.
It requires a published release; until the first release is published, use the
source-build instructions above.

```sh
curl -fsSL https://github.com/sean-brydon/Tailmux/releases/latest/download/install.sh -o /tmp/install-tailmux.sh
sh /tmp/install-tailmux.sh
```

Use `--version vX.Y.Z` or `--bin-dir /path/to/bin` to override the defaults. The
installer does not change your shell configuration, enroll Tailscale nodes, or
install Herdr on remote machines. Run `tailmux stop` after an update to restart
the daemon on the new binary while retaining its identities.

Maintainer checks and packaging:

```sh
python3 scripts/test-installer.py
python3 scripts/check-docs.py
./scripts/build-release.sh v0.1.0
```

The packaging script creates archives for macOS/Linux on amd64/arm64, embeds the
version, and writes `checksums.txt` plus `install.sh` into `dist/`. Pushing a
`vX.Y.Z` tag triggers the **Release** workflow, which tests and publishes these
assets. Building assets locally does not publish them.

## Terminal with a box picker (tmux or Zellij)

```sh
tailmux terminal                           # saved default, otherwise tmux
tailmux terminal personal/devbox           # open a particular box
tailmux terminal --backend zellij          # override for this launch only
tailmux terminal --default zellij          # save default; does not launch
tailmux terminal --default tmux            # switch the saved default back
```

Install `fzf` and your chosen multiplexer locally. This integration is tested with tmux 3.7c, Zellij 0.45.1, and fzf 0.74.3. Zellij's integration requires its modern `list-panes --json --tab` and background-session CLI controls. Both backends use **tmux on the remote host** for persistent shells; remote Zellij is not required. Host SSH settings and authentication remain shared with `tailmux ssh`.

**Alt+B** opens a fuzzy box picker in either backend. Zellij also supports **Ctrl+B** in normal mode or **F2** when unlocked; these avoid terminals that do not send Option/Alt as Meta. With tmux, **Ctrl+B, B** also opens it. Search visible machines across your profiles and press Enter to connect or switch to an existing host window/tab. Offline machines are labeled; reachability and SSH permissions still determine whether a connection can succeed. The initial home pane also offers Enter to open the picker.

New split panes inside a host window/tab connect to that same host, each with its own persistent remote shell. A new unnamed Zellij tab shows the box chooser rather than a local shell. Host and local windows/tabs can be renamed freely: Tailmux keeps their canonical route separately, so new panes and picker selection still use the right machine. Invalid or unrecognized tabs return to the chooser.

Each configuration directory gets its own local tmux socket/Zellij session. Tailmux writes its generated Zellij configuration and private routing metadata under `TAILMUX_HOME/terminal/` and does not modify your normal multiplexer config. Reopening reuses the same local session. The picker does not stop or migrate processes when you switch boxes.

Remote shells run on a dedicated tmux server named `tailmux`, with its status bar hidden and **Ctrl+A** as its prefix. Your existing remote tmux sessions/configuration and Herdr sessions remain independent. Disconnecting shows a prompt: Enter reconnects to the same remote shell, while `q` closes the local pane. Closing local panes leaves their remote sessions available; they are not automatically deleted. This first version does not provide a remote-session cleanup browser.

Detach the local view with **Ctrl+B, D** in tmux or **Ctrl+O, D** in Zellij. Reattach with `tailmux terminal`. If an application needs the remote tmux prefix literally, press **Ctrl+A twice**. Zellij uses simplified UI and host pane titles. Ctrl+B in normal mode opens the picker instead of its usual tmux-mode shortcut. Other normal multiplexer shortcuts remain available. Backend preference is stored as `terminal_backend` in `config.json`; `--backend` always overrides it for one launch.

## Local port forwarding and named development URLs

```sh
tailmux forward personal/devbox 3000             # localhost:3000 → remote localhost:3000
tailmux forward personal/devbox 3000-3010        # inclusive range (up to 100 ports)
tailmux forward personal/devbox 8080:3000        # local:remote mapping
tailmux loopback setup personal/devbox            # configures devbox.test
tailmux loopback setup work/buildbox              # configures buildbox.test
tailmux forward personal/devbox 3000-3005 --name devbox.test
tailmux forward work/buildbox 3000-3005 --name buildbox.test
tailmux forwards                                # IDs, routes and status
tailmux forwards --json
tailmux unforward <id>
```

Without `--name`, forwarding carries arbitrary TCP unchanged over SSH to the remote machine's loopback interface. Using the same local and remote port preserves `localhost` URLs, including OAuth callbacks registered for localhost. Raw TCP listeners bind only to `127.0.0.1`; they are not exposed on your LAN. SSH must allow local forwarding; no Tailmux installation or additional service is needed remotely.

With `--name`, Tailmux runs an HTTP reverse proxy on a dedicated `127.77.x.y` address for that canonical box. Run `tailmux loopback setup <host>` first; it uses `<host>.test` by default, updates this machine's `/etc/hosts`, and configures the local loopback alias. Different boxes can then use the same port without colliding: open `http://devbox.test:3000` and `http://buildbox.test:3000`. Use `tailmux loopback list` to inspect assignments. `.localhost` names are rejected because browsers force them to `127.0.0.1`, bypassing per-box isolation.

Loopback setup changes only your local computer; it does not modify the remote host. It needs administrator access and, on macOS, the address alias does not survive reboot. Rerun the same setup command before resuming named forwards; Tailmux does not install a persistent LaunchDaemon. A non-Tailmux process listening on `0.0.0.0:<port>` can still conflict because a wildcard listener covers every local address. Raw TCP and managed public forwards retain `127.0.0.1` bindings.

Named forwards rewrite HTTP `Location`, `Content-Location` and `Refresh` redirects pointing directly to localhost, 127.0.0.1 or ::1 on ports in that forward group. JSON URL strings are also rewritten, covering common authentication responses, with a 2 MiB buffering limit. Loopback cookie domains are removed to make cookies host-only; Secure, HttpOnly and SameSite attributes are preserved. Requests use the upstream localhost Host header; matching Origin and Referer URLs are translated back. WebSocket upgrades and streaming responses use Go's reverse proxy. Use `--no-rewrite` to disable response, Origin and Referer rewriting.

This does not rewrite HTML/JavaScript, external URLs containing OAuth `redirect_uri` parameters, or HTTPS URLs. OAuth provider callback allowlists, HTTPS requirements and application origin validation may still require configuration. Unlike `localhost`, a `.test` HTTP origin is not automatically a secure context; Secure cookies, service workers and OAuth flows may need HTTPS, a public URL, or application settings. For a strict localhost-only login flow, use the unnamed forward with the original port. Named mode serves HTTP; use raw TCP for end-to-end TLS or non-HTTP protocols. Two raw forwards cannot own the same local port. Unknown HTTP hostnames are rejected.

Forwards run in the local daemon after the CLI exits. `--save NAME` persists a group and restores it when networking next starts. SSH failures retry with capped backoff while keeping the group's local ports reserved. `tailmux forward --resume NAME` retries a saved group; `unforward` stops and forgets it. `tailmux stop` stops networking while preserving saved definitions. Port ranges reserve their ports together, and SSH uses your normal unlocked agent in batch mode.

After upgrading, run `tailmux stop` once so the next command starts a daemon with isolated-loopback support. An existing saved named forward may remain failed until you run `tailmux loopback setup` for its host/name and then `tailmux forward --resume <saved-name>`.

### Saved forwards and public URLs

```bash
tailmux forward personal/devbox 3000 --name devbox.test --save web
tailmux forward --resume web
tailmux ports personal/devbox --forward

# Use a configured provider account/domain; one port per public URL.
tailmux forward personal/devbox 13000:3000 --cloudflare preview-tunnel \
  --url https://preview.example.com --save preview
tailmux forward personal/devbox 13000:3000 --ngrok \
  --url https://YOUR-ASSIGNED-DOMAIN.ngrok-free.app --save preview
```

Choose one provider example. Tailmux owns the connector process and rewrites direct localhost redirects to the explicit HTTPS origin. `unforward preview` stops the connector and removes the saved group; external DNS/domain registrations remain with the provider. A stable URL still requires the app and connector to be online. Provider authentication, domain setup and OAuth callback registration are not automatic. See the [forwarding guide](docs/content/docs/forwarding.mdx).

### Host setup

```bash
tailmux setup check --all
tailmux setup install personal/devbox --tmux --ports
tailmux setup orca personal/devbox --local-port 16768
```

Checks diagnose host tools; installation explicitly adds selected prerequisites. Orca setup defaults to a review-only service plan; `--apply` writes a disabled service after Orca is installed. Firewall persistence and boot startup remain host-specific. See [host setup](docs/content/docs/setup.mdx) and [port discovery](docs/content/docs/ports.mdx).

## Workflow guides

- [Terminal and box picker](docs/content/docs/terminal.mdx)
- [Port forwarding and local URLs](docs/content/docs/forwarding.mdx)
- [Troubleshooting](docs/content/docs/troubleshooting.mdx)

After upgrading, reopen `tailmux terminal` to regenerate its configuration. Existing windows/tabs whose names still match `profile/host` migrate automatically on first use. Tailmux cannot infer the host of a tab that was already renamed before this upgrade; open that host from the picker once to establish its routed tab. Later renames are safe. A smaller attached client can constrain a Zellij tab; detach unused clients.

Zellij reuses a **running** local session when you detach. Closing a tab with Ctrl+T, then X removes it. After closing the last tab, the next `tailmux terminal` starts a fresh session. Tailmux disables disk-layout resurrection so previously closed tabs cannot return from an old snapshot. Remote tmux shells remain on their hosts; this does not stop remote processes.

## Orca serve

Native remote Orca runtimes are supported through SSH tunnels across profiles:

```sh
tailmux orca serve lab/worker
tailmux orca status lab/worker
tailmux orca exec lab/worker -- terminal list --json
```

`serve` starts the preconfigured remote `tailmux-orca.service` user unit; it does not install Orca or restart active agents. `connect` pairs an already-running runtime. The remote server must advertise a unique laptop loopback port and its listener must be protected by a host firewall. See the [complete setup guide](docs/content/docs/orca.mdx). Orca owns remote projects, terminals and credentials; Tailmux owns the transport.

### Open this computer in the terminal

```bash
tailmux terminal local
tailmux terminal --backend tmux local
tailmux terminal --backend zellij local
```

The box picker lists `local` (this machine) first, followed by remote targets with aligned online/offline and saved labels. Local access works without a Tailscale profile or SSH connection. It opens your `$SHELL` as a login shell (falling back to `/bin/sh`); new panes in that tab/window also run locally after you rename it. Exiting the shell closes that pane.

`local` is reserved by `terminal` for this computer. Use the full `profile/local` target if a remote machine is also named local. Existing shortcuts open the same picker from local and remote tabs.
