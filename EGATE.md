# Integrate an application with egate

Use egate as the email gateway for this application. egate sends through Resend. The calling application talks only to egate and never needs the Resend API key.

## Obtain and store credentials

1. An egate administrator signs in at `{EGATE_HOST}/login` and registers this application under **Apps**.
2. Copy the generated app key immediately. egate shows it only once. If it is lost, revoke the app and register a replacement.
3. Set `EGATE_HOST` to the absolute base URL of the egate deployment, such as `https://egate.example.com`. Do not append `/v1/email`.
4. Set `EGATE_API_KEY` to the generated `eg_...` app key. Keep it in server-side secret storage. Never commit, log, or expose it to browser code.

These two variables are for the **calling application**. The egate server is separately configured with `EGATE_RESEND_API_KEY` and an admin login.

## HTTP contract

Send `POST {EGATE_HOST}/v1/email` with `Authorization: Bearer {EGATE_API_KEY}` and `Content-Type: application/json`.

```sh
curl "$EGATE_HOST/v1/email" \
  -H "Authorization: Bearer $EGATE_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "from": "Example Team <mail@example.com>",
    "to": "recipient@example.com",
    "subject": "Welcome",
    "text_body": "Welcome to Example.",
    "html_body": "<p>Welcome to Example.</p>",
    "reply_to": "support@example.com"
  }'
```

`from`, `to`, and `subject` are required nonempty strings. Supply at least one of `text_body` and `html_body`; both are useful for accessibility. `reply_to` is optional. This gateway accepts one recipient string per request. The `from` address or domain must be allowed by the egate server's Resend account.

A successful send returns Resend's HTTP status and JSON response, typically an object with an `id`. An invalid request returns 400; a missing, invalid, or revoked app key returns 401; an unavailable Resend API returns 502. Other Resend error status codes and bodies pass through. Handle non-2xx responses without showing raw provider errors to end users. Set an application timeout and avoid blind retries, since an uncertain failure can result in duplicate email.

## Go integration

For Go applications, use the egate SDK:

```sh
go get github.com/englandsystems/egate
```

```go
package notifications

import (
    "context"
    "fmt"
    "os"

    "github.com/englandsystems/egate/sdk"
)

func SendWelcome(ctx context.Context, recipient string) error {
    client, err := sdk.NewClient(os.Getenv("EGATE_HOST"), os.Getenv("EGATE_API_KEY"))
    if err != nil {
        return fmt.Errorf("configure egate: %w", err)
    }
    _, err = client.SendEmail(ctx, sdk.Email{
        From: "Example Team <mail@example.com>",
        To: recipient,
        Subject: "Welcome",
        TextBody: "Welcome to Example.",
        HTMLBody: "<p>Welcome to Example.</p>",
    })
    if err != nil {
        return fmt.Errorf("send welcome email: %w", err)
    }
    return nil
}
```

`SendEmail` returns `(*sdk.Response, error)`. For a non-2xx response, the error is `*sdk.APIError` and the response still contains the status, headers, and body. Transport failures can return no response. The SDK has a 15-second HTTP timeout by default; `SetHTTPClient` accepts a custom `*http.Client`. Pass request context through from HTTP handlers.

## Instructions for a coding assistant

When integrating egate into another repository, inspect that application's existing email and configuration paths. Add `EGATE_HOST` and `EGATE_API_KEY` to its server-side configuration, use the SDK for Go or the HTTP contract for other languages, and route the application's email events through egate. Preserve its existing email content and error handling requirements. Keep credentials out of source control and client-side bundles. Use mock transports or a local test server for tests; never send real mail from tests. Do not call Resend directly from the calling application.

The egate administrator can revoke an app from the Apps page, which immediately makes its key unusable. Register a replacement app to rotate a key.
