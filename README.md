# egate

A small HTTP email gateway backed by SQLite and Resend. Administrators sign in and register apps in a browser; each app receives a revocable key that it uses to send mail through `POST /v1/email`.

## Local setup

Requires Go 1.26 or newer and a Resend API key.

```sh
go run . --init-env config/.env
```

Edit `config/.env`: replace the starter administrator password and invalid Resend API key. The initializer creates the directory and file with owner-only permissions and refuses to overwrite an existing file. Then run:

```sh
go run . --env config/.env
```

Open `http://127.0.0.1:8119` to sign in and register apps at `/admin`. The admin page shows the gateway host and complete email endpoint alongside app keys. The app creates `data/main.sqlite` and its tables on startup. It also synchronizes the configured admin username and password into the database on every startup, so update `config/.env` to rotate them.

The service reads process environment by default. For local runs, `--env PATH` loads a file. `schema.json` lists all supported settings. The default port is `8119`; the default bind address is `0.0.0.0`. Set `EGATE_HOST=127.0.0.1` in `config/.env` if a local run should accept only local connections.

## Uppr deployment

Uppr builds the root `Dockerfile` and uses the `EGATE_PORT` declaration in `schema.json` to map the container port. It takes the value from `config/.env`, or uses the schema's default of `8119`. Uppr mounts the project’s `config/` and `data/` directories at `/app/config` and `/app/data` and passes `config/.env` through Docker `--env-file`. Egate reads those process environment variables, binds to `0.0.0.0` on the configured port, and uses `/app/data/main.sqlite`. The default relative database path works because the container starts in `/app`.

Before deploying, set `EGATE_ADMIN_USERNAME`, a strong `EGATE_ADMIN_PASSWORD`, and a real `EGATE_RESEND_API_KEY` in Uppr’s `config/.env`. If Uppr creates blank entries from `schema.json`, fill those three in. Keep `EGATE_DB=./data/main.sqlite` and `EGATE_HOST=0.0.0.0` if those settings are present. Set `EGATE_PORT` to the desired listen port, or leave it unset to use `8119`; Uppr maps the same port to the container. After changing the schema port or route, run `uppr install`, then `uppr launch --all`. Uppr retains the mounted directories when the container is replaced; back up `data/` to preserve registered apps, keys, and sessions.

Put the public endpoint behind TLS and have the proxy set `X-Forwarded-Proto: https` so administrator session cookies receive the `Secure` flag. Login rate limiting uses the direct peer IP and does not trust `X-Forwarded-For`; limit abusive clients at the reverse proxy or restrict access to `/login` and `/admin`. Uppr uses the launchpad manifest domain when `schema.json` has no `domains` entry; add your real domain with `uppr domains` if you want this repository to own its Caddy route. Uppr applies its default per-IP rate limit when `rate_limit` is absent.

## Docker without Uppr

```sh
make docker
```

On first use, `make docker` creates `config/.env` with starter values and stops until you replace its password and Resend API key. Then rerun `make docker`. The target passes `config/.env` through Docker `--env-file`, mounts `config/` read-only and `data/` read-write, publishes port `8119`, and restarts the named `egate` container unless stopped. Use `make docker-logs` and `make docker-stop` to inspect or stop it.

## Send an email

After registering an app at `/admin` and copying its one-time key:

```sh
curl http://127.0.0.1:8119/v1/email \
  -H 'Authorization: Bearer eg_YOUR_KEY' \
  -H 'Content-Type: application/json' \
  -d '{"from":"sender@example.com","to":"recipient@example.com","subject":"Hello","text_body":"Sent through egate"}'
```

The gateway passes through Resend’s HTTP status and response body. Five failed administrator logins from one IP in a rolling 24 hour window cause a 24 hour ban by default; `EGATE_LOGIN_MAX_ATTEMPTS` changes the threshold.

## Go SDK and agent guide

Install `github.com/englandsystems/egate` and import `github.com/englandsystems/egate/sdk`. Supply the gateway URL and the registered app key to the SDK:

```go
client, err := sdk.NewClient("https://egate.example.com", os.Getenv("EGATE_API_KEY"))
if err != nil {
    log.Fatal(err)
}
_, err = client.SendEmail(context.Background(), sdk.Email{
    From: "sender@example.com", To: "recipient@example.com",
    Subject: "Hello", TextBody: "Sent through egate",
})
```

The SDK’s `EGATE_API_KEY` and gateway URL belong to the **calling application**; they are not settings for the egate server. In the calling app, `EGATE_HOST` is the base URL shown in the admin page. On the egate server, the setting with the same name controls the listen address. The repo includes [EGATE.md](EGATE.md), a guide for an LLM or developer integrating egate into another app. Run `egate teach ./EGATE.md` in that app to copy the guide from the current binary; repeating the command updates it.

## Provider migration

Existing Postmark deployments must set `EGATE_RESEND_API_KEY` in `config/.env` and verify the sender domain in Resend before restarting. Existing active keys are imported as registered apps on startup, so calling applications can keep their current `eg_` keys. The old `EGATE_POSTMARK_API_KEY` setting is no longer used.
