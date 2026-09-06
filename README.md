# Tailmux

A Go CLI for reaching development machines across separate Tailscale accounts with persistent terminals, a cross-host box picker, and local port forwarding.

Profile names and hostnames are supplied at runtime. Each profile gets its own embedded Tailscale (`tsnet`) node and private state directory. Your system Tailscale connection stays independent.

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

Stopping the daemon closes local SSH connections and stops forwards, which are not automatically restored. Remote Herdr sessions continue, but ordinary nonpersistent SSH commands may terminate when disconnected.

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

### Public preview URLs

Cloudflare and ngrok can publish a specific raw forward, for example `tailmux forward personal/devbox 13000:3000`. Cloudflare named tunnels provide a stable custom hostname; ngrok supports an assigned account domain. Quick Cloudflare tunnels use temporary URLs. See the [public URL guide](docs/content/docs/forwarding.mdx#public-urls-with-cloudflare-or-ngrok) for setup, redirect limitations and cleanup.

The proposed `--cloudflare` and `--ngrok` flags are not implemented yet; use the provider CLI alongside Tailmux. A stable hostname still requires the app, forward and connector to stay running.

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

New split panes inside a host window/tab connect to that same host, each with its own persistent remote shell. A new unnamed Zellij tab shows the box chooser rather than a local shell. Keep host window/tab names unchanged: those names determine which host new panes connect to. Invalid or unrecognized names return to the chooser.

Each configuration directory gets its own local tmux socket/Zellij session. Tailmux writes its generated Zellij configuration under `TAILMUX_HOME/terminal/` and does not modify your normal multiplexer config. Reopening reuses the same local session. The picker does not stop or migrate processes when you switch boxes.

Remote shells run on a dedicated tmux server named `tailmux`, with its status bar hidden and **Ctrl+A** as its prefix. Your existing remote tmux sessions/configuration and Herdr sessions remain independent. Disconnecting shows a prompt: Enter reconnects to the same remote shell, while `q` closes the local pane. Closing local panes leaves their remote sessions available; they are not automatically deleted. This first version does not provide a remote-session cleanup browser.

Detach the local view with **Ctrl+B, D** in tmux or **Ctrl+O, D** in Zellij. Reattach with `tailmux terminal`. If an application needs the remote tmux prefix literally, press **Ctrl+A twice**. Zellij uses simplified UI and host pane titles. Ctrl+B in normal mode opens the picker instead of its usual tmux-mode shortcut. Other normal multiplexer shortcuts remain available. Backend preference is stored as `terminal_backend` in `config.json`; `--backend` always overrides it for one launch.

## Local port forwarding and named development URLs

```sh
tailmux forward personal/devbox 3000             # localhost:3000 → remote localhost:3000
tailmux forward personal/devbox 3000-3010        # inclusive range (up to 100 ports)
tailmux forward personal/devbox 8080:3000        # local:remote mapping
tailmux forward personal/devbox 3000-3005 --name devbox.localhost
tailmux forward work/buildbox 3000-3005 --name buildbox.localhost
tailmux forwards                                # IDs, routes and status
tailmux forwards --json
tailmux unforward <id>
```

Without `--name`, forwarding carries arbitrary TCP unchanged over SSH to the remote machine's loopback interface. Using the same local and remote port preserves `localhost` URLs, including OAuth callbacks registered for localhost. Local listeners bind only to `127.0.0.1`; they are not exposed on your LAN. SSH must allow local forwarding; no Tailmux installation or additional service is needed remotely.

With `--name`, Tailmux runs an HTTP reverse proxy. Different names can share the same local port across boxes. Open `http://devbox.localhost:3000` in your browser. Modern browsers resolve `.localhost` names to loopback; other clients may need explicit resolution (for example, curl's `--resolve devbox.localhost:3000:127.0.0.1`). Custom names such as `devbox.local` or `dev.example.test` work when your resolver maps them to `127.0.0.1`. Add `127.0.0.1 devbox.local` to `/etc/hosts` or configure your local DNS; Tailmux does not change system DNS. `.local` otherwise belongs to multicast DNS and is not automatically registered by Tailmux.

Named forwards rewrite HTTP `Location`, `Content-Location` and `Refresh` redirects pointing directly to localhost, 127.0.0.1 or ::1 on ports in that forward group. JSON URL strings are also rewritten, covering common authentication responses, with a 2 MiB buffering limit. Loopback cookie domains are removed to make cookies host-only; Secure, HttpOnly and SameSite attributes are preserved. Requests use the upstream localhost Host header; matching Origin and Referer URLs are translated back. WebSocket upgrades and streaming responses use Go's reverse proxy. Use `--no-rewrite` to disable response, Origin and Referer rewriting.

This does not rewrite HTML/JavaScript, external URLs containing OAuth `redirect_uri` parameters, or HTTPS URLs. OAuth provider callback allowlists, HTTPS requirements and application origin validation may still require configuration. For a strict localhost-only login flow, use the unnamed forward with the original port. Named mode serves HTTP; use raw TCP for end-to-end TLS or non-HTTP protocols. Two raw forwards cannot own the same local port. Unknown HTTP hostnames are rejected.

Forwards run in the local daemon after the CLI exits. They stop with `unforward`, `tailmux stop`, or daemon shutdown; they are not automatically restored. SSH failures appear in `forwards` and release the ports; recreate the forward to reconnect. Port ranges are reserved together: a conflict rejects the new group without disturbing existing forwards. Each group uses one SSH connection and private temporary Unix sockets. Forward creation uses your existing SSH configuration and agent in batch mode, so unlock the agent first. Automatic process selection is not yet implemented.

## Workflow guides

- [Terminal and box picker](docs/content/docs/terminal.mdx)
- [Port forwarding and local URLs](docs/content/docs/forwarding.mdx)
- [Troubleshooting](docs/content/docs/troubleshooting.mdx)

After upgrading, reopen `tailmux terminal` to regenerate its configuration. A smaller attached client can constrain a Zellij tab; detach unused clients. Preserve full host tab/window names because new split panes use them for routing.

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

The box picker lists `local` (this machine) first, followed by remote targets with aligned online/offline and saved labels. Local access works without a Tailscale profile or SSH connection. It opens your `$SHELL` as a login shell (falling back to `/bin/sh`); new panes in the `local` tab/window also run locally. Exiting the shell closes that pane. Keep the tab/window named `local` so new panes retain this routing.

`local` is reserved by `terminal` for this computer. Use the full `profile/local` target if a remote machine is also named local. Existing shortcuts open the same picker from local and remote tabs.
