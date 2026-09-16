# Transition table

| Inbound on conn_id | `awaitingInit` | `handshakeComplete` | `open` | `closed` |
|---|---|---|---|---|
| `noise_init` | run handshake (below) | close(4421), → closed (`reason=noise_init_in_handshake_complete`) | **re-key responder; peer-static check; CipherState swap; state stays `open` (#453)** | drop |
| `noise_resp` (phone is never the writer) | close(4421), → closed | close(4421), → closed | close(4421), → closed | drop |
| `noise_msg`, decrypt succeeds | close(4410), → closed (no CipherStates yet; retryable — conn id the manager holds no session for, #2488) | sealed `auth.invalid_token` + close(4401), → closed | **dispatch via handler chain; AEAD-sealed reply emitted; state stays `open`** | drop |
| `noise_msg`, decrypt fails / no CipherStates | close(4410), → closed (retryable — conn id the manager holds no session for, #2488) | close(4421), → closed | **close(4421), → closed (AEAD-failure teardown; session entry dropped — also fires on stale-key frames after a #453 swap)** | drop |
| Unknown `type` / bad `v` / malformed JSON / oversized `data` | close(4421), → closed | close(4421), → closed | close(4421), → closed | drop |
