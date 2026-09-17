# Markdown-viewer

An out-of-the-box, high-performance local Markdown real-time preview tool. You can use it to map any directory and it will intelligently detect all `.md` files in that directory.

It supports code highlighting and math formulas and other features optimized for the experience.

## Usage

Download the version for your system from the [Releases](https://github.com/sxyazi/markdown-viewer/releases) page, then run it as follows.

```bash
./markdown-viewer <path>
```

Done! Now you can open http://127.0.0.1:3000 and see it.

## Login required

~~Opening the address immediately displays documents.~~
The viewer now requires an account. User password hashes and expiring sessions
are stored in SQLite 3.53.4. Create the initial account with `--create-user`
(password on stdin); tokens expire after 8 hours by default and logout revokes
the current session. See [authentication and account setup](deploy/AUTH.md).
The database must be outside the document root; Docker persists it in `auth-data`.

## Docker and LAN access

The production UI uses same-origin API requests, including when Docker maps port
3000 to a different host port. See [Docker deployment](deploy/README.md) for
~~read-only document mounts~~, the Hackintosh deployment, verification and rollback.
Document mounts are now writable for authenticated synchronization. The
[document manager](/documents) supports same-path Markdown overwrite, historical
content preview/download and restore. See [document synchronization](deploy/SYNC.md).
