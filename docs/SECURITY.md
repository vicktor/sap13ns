# Security

## Reporting a vulnerability

Please report vulnerabilities privately:
- through GitHub's **Report a vulnerability** button on the repository's Security tab;
- or by e-mail to **victor@bauber.xyz**, with "sap13ns security" in the subject.

Include the version (`sap13ns version`), the steps to reproduce, and the impact you see. Please don't open public issues for vulnerabilities.

We aim to acknowledge reports within three working days and to publish a fix, with a signed release and a security advisory, as soon as it is ready. Reporters are credited unless they prefer otherwise.

Supported versions: the latest release. Before 1.0, fixes are not backported.

## Security model

sap13ns assumes the server, its operator and its administrators are trusted. Everyone else is not: visitors, commenters, authors and contributors, plugin authors, AI providers and the content they return, and anything fetched from the network. The measures below enforce that boundary.

### Accounts and sessions
- **Passwords:**
  - hashed with Argon2id (time 2, memory 32 MiB) with a per-password random salt;
  - at least 10 characters, and the most common passwords are refused;
  - a password change or reset closes the user's other sessions.
- **Second factor:**
  - required for staff by default: TOTP, or a passkey (WebAuthn with user verification);
  - enrolling the first second factor closes the user's other sessions;
  - adding a passkey or removing the last second factor requires a recent sign-in or the password.
- **Rate limits and lockouts:**

  | Action | Limit |
  |---|---|
  | Sign-in, per IP | 20 per 15 minutes |
  | Sign-in, per account | 8 per 15 minutes |
  | Second step | 6 per 15 minutes |
  | Comments, per IP | 6 per 10 minutes |
  | Registration and password reset | 5 per hour |

  Repeated failures lock the account for a while. Every outcome is written to the audit log.
- **Sessions:**
  - random tokens stored only as hashes;
  - `HttpOnly` cookies, `Secure` over https, `SameSite=Lax`;
  - idle and absolute lifetimes;
  - users can list their sessions and end them.
- **Capabilities:** every admin route checks a capability of the user's role (administrator, editor, author, contributor, subscriber), and object-level checks keep authors to their own content.

### Requests
- **Cross-site requests:**
  - state-changing requests must pass Go's cross-origin protection (`Sec-Fetch-Site` / `Origin`) and carry the session's CSRF token;
  - JSON endpoints require `application/json`.
- **Response headers:**
  - a strict Content Security Policy: `script-src 'self'`, `style-src 'self'`, no inline scripts, styles or style attributes anywhere, `frame-ancestors 'none'`, `object-src 'none'`;
  - also HSTS (over https), `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy`, `Permissions-Policy` and `Cross-Origin-Opener-Policy`.
- **Redirects:** after sign-in, only local paths are accepted. They are parsed, and control characters and backslashes are rejected.
- **Forwarded headers:** `X-Forwarded-For` is honoured only from configured proxies or over the Unix socket.

### Content
- Markdown is rendered with goldmark and sanitised with bluemonday. The same applies to plugin output, AI answers and imported WordPress content.
- **Uploads:**
  - the content type is detected from the bytes and must match the file extension;
  - files are stored under random names and served with `Content-Security-Policy: sandbox` and `nosniff`;
  - anything other than images, audio, video and PDF is served as a download.
- Archive extraction (backups, the importer, plugin packages) refuses absolute paths, `..`, links and special files, and enforces size limits.

### Outbound requests
All server-side fetches go through an SSRF guard that resolves the host and refuses loopback, private, link-local, CGNAT, multicast and cloud metadata addresses, on every redirect as well. This covers plugin HTTP calls, imported media, AI image URLs and release downloads. Release downloads are further restricted to GitHub hosts over https.

### Plugins
- Plugins are WebAssembly modules run by wazero:
  - no filesystem, sockets, environment or clock beyond what WASI provides;
  - memory caps, per-call timeouts and a limit on concurrent instances;
  - a plugin is paused automatically after repeated failures.
- **Permissions** are explicit and shown at approval: private key-value storage, read-only published posts, HTTP to named hosts only.
- **Approval** pins the module's SHA-256 and the granted permissions. Any change disables the plugin until it is reviewed again.
- **Output** is sanitised before it reaches a page. Head tags are limited to safe `<meta>` names; `http-equiv` is never allowed.

### Secrets and data
- The instance secret (`SAP13NS_SECRET_KEY`) signs tokens and encrypts stored secrets, such as AI provider keys, with AES-256-GCM. Secrets are never shown again in the admin.
- **Backups** can be encrypted with AES-256-GCM (STREAM construction, Argon2id key derivation). The passphrase is read from a file or the environment, never from a command-line flag.
- **Updates** install only releases whose `SHA256SUMS` carries a valid Ed25519 signature from the key compiled into the binary.
- **Analytics** store only daily aggregates. Visitors are counted with a salt that rotates daily and lives only in memory: no IP address, user agent or identifier is persisted.
- **Public pages** make no third-party requests and set no tracking cookies.

### Deployment
The systemd unit in [`deploy/systemd`](../deploy/systemd/sap13ns.service) runs sap13ns unprivileged and sandboxed (see [DEPLOY.md](DEPLOY.md#hardening-notes)). nginx terminates TLS and talks to sap13ns over a Unix socket (`0660`, owned by the `sap13ns-web` group, which nginx joins); nginx cannot read the env file or the data directory.
