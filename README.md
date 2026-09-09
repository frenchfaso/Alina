# Alina

**Give an agent somewhere to grow.**

Alina lives on your device and makes it her workspace. Talk to her through
Telegram or a terminal. She works with files, uses installed tools, searches
the web, and carries useful context from one conversation to the next.

The idea is to give an agent continuity, some freedom to explore, and a small
enough harness to leave room for her own way of being useful.

- **Remember what matters.** Notes fade from attention over time and return to
  focus when recalled. A separate archive preserves what happened, so the past
  remains searchable even as the working context moves on.
- **Leave time to dream.** At night, Alina can revisit experiences and open
  questions, and revise a short `soul.md`: her evolving sense of how to approach
  things.
- **Make room for curiosity.** With autonomy enabled, she can keep intentions,
  schedule her own next steps, explore, and save useful procedures. You choose
  the scope and budget. A new message can steer her work; `/stop` can halt it.
- **Keep one Alina.** People in the same family can share remembered context.
  Personal and family memory have their own scopes, while one shared soul
  develops through reflection on those different experiences.

Conversation, scheduled work, exploration and dream all use the same agent loop.
A Go daemon, a few native tools, SQLite and readable Markdown give that loop
somewhere to work. The device supplies the rest.

**Start simple, stay simple. Less is more.**

Android/Termux first, with Linux, macOS and BSD targets. Build with Go 1.26+:

```sh
git clone https://github.com/frenchfaso/Alina.git
cd Alina
go build -trimpath -o alina .
./alina setup
./alina serve       # run in the background
./alina chat        # or talk through Telegram
```

Guided setup connects your model and Telegram. Supports ChatGPT subscriptions,
OpenCode Go, web search, photos and files. Quick setup asks consent for arbitrary
downloads and package installation. The harness runs locally; model requests
go to your configured provider.

An experimental POC for trusted people and devices. Configured users share one
OS account. [MIT licensed](LICENSE).

[Setup and limits](docs/poc.md) · [How Alina works](internal/alina/procedures/harness.md)
· [Operations](docs/operations.md) · [Verification](docs/verification.md)

Inspired by [Hermes](https://github.com/NousResearch/hermes-agent),
[OpenClaw](https://github.com/openclaw/openclaw) and
[pi](https://github.com/badlogic/pi-mono/tree/main/packages/coding-agent).
