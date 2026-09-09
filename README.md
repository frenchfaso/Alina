# Alina

A small personal device operator in Go. Android/Termux first; Linux, macOS
and BSD targets. **Start simple, stay simple. Less is more.**

Talk through Telegram or a plain terminal chat. Alina uses installed commands,
a few native tools, integrated web search and web reading. Quick setup asks
consent for arbitrary downloads and package installation; stricter network
approval is available.

```sh
go build -trimpath -o alina .
./alina setup
./alina serve        # starts in the background, or reports the existing daemon
./alina chat
# ./alina serve stop
```

Providers: a dedicated ChatGPT subscription login or OpenCode Go. Telegram
supports multiple trusted people, native families, photos and files. Its small
command menu includes `/model`, `/think`, `/status`, `/stop` and `/resume`;
ChatGPT model/reasoning choices come from the provider catalog and are personal.
Families share memories; a single global soul and nightly dream shape Alina's orientation.
A SQLite event archive and attention-based notes preserve continuity; working
context compacts automatically under pressure, independently of dream.

Alina can read its bundled Markdown manual, inspect its live configuration and
logs, and apply user-requested settings through a controlled self-restart with
startup rollback. No plugin runtime, mandatory external converter, permanent
supervisor or boot autostart is required. This remains an experimental POC.

State uses the OS user config directory (`~/.config/alina` on Termux/Linux;
`~/Library/Application Support/alina` on macOS), or `ALINA_HOME`. Setup and local
configuration writes require the daemon stopped. Configured people share a
trusted OS account; family memory separation is not an OS security boundary.

See [usage and limits](docs/poc.md), [operations and debugging](docs/operations.md),
[the harness manual](internal/alina/procedures/harness.md),
[verification](docs/verification.md) and [memory research](docs/research-memory.md).
MIT licensed: [LICENSE](LICENSE).
