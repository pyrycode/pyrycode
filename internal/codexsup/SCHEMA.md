# Codex app-server protocol schema

`codex_app_server_protocol.schemas.json` is the wire schema of the Codex
app-server at **Codex 0.156.1**, the version pyrycode targets. It is the
contract for the Codex client and for the fake Codex binary's method-name
test. Do not edit it by hand.

Regenerate it when the pinned Codex version changes:

```sh
npx -y @openai/codex@0.156.1 app-server generate-json-schema --out <dir>
cp <dir>/codex_app_server_protocol.schemas.json internal/codexsup/
```

Only the single-file bundle is committed. The rest of the generated directory
repeats the same definitions split per type.

After regenerating for a new pin, `make check` is the wire-shape check:
`TestRequestParamsMatchSchema` (`schema_params_test.go`) validates the params
every `clientRequests` method actually sends against that method's definition
in the new schema. A failure there names the method and field that moved, and
means pyry's encoding has to change along with the pin, not just the constant.
