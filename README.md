# Alina

A small personal device operator in Go. Android/Termux first; Linux, macOS
and BSD targets. **Start simple, stay simple. Less is more.**

The POC runs as a service. Talk to it through Telegram or its plain terminal
chat. It uses installed shell tools, with approval for network access and
package manager operations. Integrated search supports OpenAI, Tavily and Brave.

```sh
go build -o alina .
./alina setup
./alina serve
# In another terminal:
./alina chat
```

Providers: ChatGPT subscription login and OpenCode Go. Daily and weekly memory,
a SQLite archive, scheduled tasks and a nightly “dream” keep continuity.
Configuration and state use the OS user config directory (`~/.config/alina` on
Termux/Linux; `~/Library/Application Support/alina` on macOS), or `ALINA_HOME`.
The wizard can be run again while the service is stopped.

See [POC usage and limits](docs/poc.md), [test results](docs/verification.md)
and [memory research notes](docs/research-memory.md). MIT licensed: [LICENSE](LICENSE).
