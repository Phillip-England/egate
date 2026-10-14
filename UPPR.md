# Integrating an application with Uppr

Commit a production Dockerfile and schema.json at the repository root. The container must listen on 0.0.0.0 at its configured port. Store persistent data under /app/data and read environment variables from process.env. Uppr bind mounts config/ and data/ and passes config/.env through Docker --env-file. Keep config/ and data/ out of Git and Docker build context.

Example schema.json:

```json
{
  "port": { "variable": "APP_PORT" },
  "domains": ["api.example.com"],
  "rate_limit": { "events": 100, "window": "1m", "zone": "dynamic" },
  "variables": [{ "name": "APP_PORT", "example": "3000", "required": false }]
}
```

Declare the container port explicitly with "port": { "variable": "APP_PORT" }, referencing a variable in the variables array. Replace APP_PORT with the real listen-port environment variable, such as EGATE_PORT. Its example must be the app's default listening port, and the app must listen on that value at 0.0.0.0. Uppr maps Docker to the value in config/.env or the numeric example and passes the resolved value to the container. For apps without a port environment variable, use a numeric "port": 3000. A schema with a *_PORT variable but no port declaration fails before deployment. Dockerfile EXPOSE does not select the mapped port. Older schemas using PORT without an explicit port declaration remain supported.

The domains array is the source for Caddy routes. If it is absent, Uppr uses the launchpad manifest domain for older apps. Rate limiting applies per client IP through the Caddy rate_limit module. Missing rate_limit values default to 100 events per 1m in zone dynamic. Uppr prefixes the generated zone with the launchpad and app IDs to isolate apps. Set the values per application as needed. Each domain routes to the app's loopback host port. DNS must point to the host. After a schema route or port change, run uppr install, then uppr launch --all.

Create or update a schema with uppr schema generate <app>, uppr domains <app> api.example.com,alt.example.com, and uppr schema rate <app> --events 100 --window 1m --zone dynamic. Run uppr ip on the server to see its public IPv4 address for DNS. Schema changes are local Git changes; inspect and push them with uppr push <app> -m "Update routes". Store a GitHub personal access token for HTTPS remotes with uppr auth login; SSH remotes use your SSH key.

The Caddy binary must include github.com/mholt/caddy-ratelimit. Run uppr deps install or uppr deps caddy to build one with xcaddy and install it as /usr/local/bin/uppr-caddy. Uppr validates generated Caddyfiles before installing them. Use uppr caddyfile to preview the routes and uppr doctor to check the host.
