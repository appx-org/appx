# Networking & TLS

## Subdomain routing without a domain (sslip.io)

Subdomain routing (e.g. `assistum.<base>`) requires a real domain name — bare IPs
don't work because `assistum.91.98.144.204` isn't a valid hostname.
[sslip.io](https://sslip.io) provides free wildcard DNS: `anything.IP.sslip.io`
resolves to the embedded IP automatically.

Edit `/etc/appx/appx.env` and set `APPX_HOST` to the sslip.io hostname:

```bash
APPX_HOST=91.98.144.204.sslip.io
```

Delete old TLS certs so they regenerate with the wildcard SAN, then restart:

```bash
sudo rm /var/lib/appx/.appx-internals/{cert,key}.pem
sudo systemctl restart appx
```

This gives you:

- `https://91.98.144.204.sslip.io` — dashboard
- `https://assistum.91.98.144.204.sslip.io` — project subdomain
- Session cookie shared across all subdomains via `Domain=.91.98.144.204.sslip.io`

Note: the bare IP (`https://91.98.144.204`) will stop serving the dashboard.
Access via the sslip.io hostname instead.

See [docs/security/certificate_and_sslip.md](../security/certificate_and_sslip.md)
for the full analysis of certificate generation, cookie scoping, and browser
behaviour.

## Automatic TLS via Let's Encrypt

Uncomment and fill in the two variables in `/etc/appx/appx.env`:

```bash
APPX_DOMAIN=app.yourdomain.com
CLOUDFLARE_API_TOKEN=your_token_here
```

Then restart: `sudo systemctl restart appx`.

Appx requests certificates for `app.yourdomain.com` and `*.app.yourdomain.com`
via Cloudflare DNS-01 challenge. No port 80 required.

Requirements:

- Cloudflare API token with **Zone > DNS > Edit** permissions
- Domain managed by Cloudflare DNS
