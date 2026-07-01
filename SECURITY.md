# Security Policy

Vynull is a hobby project maintained on a best-effort basis. I take security reports seriously and
will do my best to respond — but please set expectations accordingly: there's no SLA and no bounty.

## Reporting a vulnerability

**Please report vulnerabilities privately — do not open a public issue.**

- Preferred: GitHub's private vulnerability reporting — **Security → Report a vulnerability** on
  this repository.
- Or email **app@vynull.dev**.

Include the affected version (release tag or commit SHA), steps to reproduce, and the impact. I'll
acknowledge as soon as I can and keep you posted on a fix and disclosure.

## Supported versions

This is a small project — only the **latest release** (and `main`) receives fixes. Older tags are
not backported.

## Scope & threat model (please read first)

Vynull is designed to run on a **trusted, link-local DJ network** — you, your laptop, and your CDJs
on a switch or a direct cable. Several things follow from that, and are **expected behaviour, not
vulnerabilities**:

- **The file server has no authentication.** Like a rekordbox / USB source, Vynull's NFS
  export is readable by anything on the same network. Don't run it on an untrusted or
  internet-facing network.
- **The HTTP API / web UI has no authentication.** It listens on `127.0.0.1` by default; passing
  `--listen 0.0.0.0:9443` exposes full control to everyone on the LAN. Only do that on a network
  you trust.
- **CDJ mode may run with elevated privileges** (to bind UDP 111). Prefer the default rekordbox
  mode, which needs no privileged ports.
- Vynull parses data from the network and from files you point it at (PDB, `master.db`, ANLZ,
  packets); it's meant for your own library and your own decks.

Reports I'm especially interested in:

- memory-safety / crash bugs reachable from **malformed network packets** or **files**
  (PDB / ANLZ / `master.db`),
- anything that lets a host **outside** the link-local network reach the file server or API,
- anything that escalates beyond the documented trust model above.
