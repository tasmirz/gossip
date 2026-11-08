# Two-host acceptance runbook

> **ACCEPTANCE GATE STATUS: OPEN (UNVERIFIED ON PHYSICAL HARDWARE)**
>
> Physical two-machine acceptance remains an **UNVERIFIED OPEN GATE** for later execution on separate physical hardware. Local multi-worktree coordination across distinct checkouts (`pkg/coord/multi_worktree_acceptance_test.go`), container network smoke tests (`scripts/coordination-container-network-smoke.sh`), and loopback mTLS/FRP integration tests (`TestRemoteMutualTLSResearchHandoff`) are automated and verified.
>
> **MANDATORY POLICY**: Do not mark P4 complete based only on mocks or same-host tests. This gate can only be closed after this procedure succeeds between separate physical computers and the completed attestation record is attached to the release review.

This procedure closes the P4 acceptance gate for a real LAN or FRP deployment. Run Host A and Host B on separate physical computers. Docker namespaces or two processes on one computer are useful tests, but do not count as this acceptance run. Use a disposable project and test certificates. Never paste private keys or project credential tokens into this record.

Before using separate hosts, run [`scripts/coordination-container-network-smoke.sh`](../scripts/coordination-container-network-smoke.sh) as a network preflight. It creates two isolated Docker network namespaces and verifies mTLS, project-scoped certificate and credential checks, revocation, and the default-off remote kill switch. It does not test an external LAN route or FRP and cannot close this acceptance gate.

The actual FRP transport/reconnect integration subtest is also available when official `frpc` and `frps` binaries are installed: set `GOSSIP_TEST_FRPC` and `GOSSIP_TEST_FRPS` to their paths, then run `go test ./pkg/coord -run '^TestRemoteMutualTLSResearchHandoff$' -count=1 -v`. This checkout passed the subtest with the official v0.72.0 Linux amd64 release after SHA-256 verification. The test runs FRP locally on loopback; it does not replace the separate-host procedure below.

## Prerequisites

- The same built `gossip` version is installed on both hosts.
- Host A can listen on the LAN, or an FRP TCP route forwards to its TLS port. Host B can reach that address.
- A private CA has issued the home server certificate, an owner client certificate, a worker client certificate, and a valid but ungranted test client certificate. Each certificate has the appropriate server/client EKU; the home certificate SAN matches the hostname or IP Host B uses.
- Host A has the server key and its owner client certificate/key. Host B has its worker certificate/key. The owner receives the worker and test client public certificates. Transfer public certificates and the worker project credential over a separate secure channel. Keep private keys on their respective hosts.
- On Windows, apply equivalent user-only ACLs to private keys and credentials. On Unix, keep them mode 0600. Record Gossip version, OS, host labels, route type (LAN or FRP), and date; keep actual private addresses out of a public report if needed.

### Generating Keys and Certificates with OpenSSL

On Host A (or an isolated provisioning workstation), run these commands in a private directory (`chmod 700 /private/gossip`):

1. **Private CA Key and Certificate (`ca.key`, `ca.crt`):**
   ```sh
   openssl req -x509 -newkey rsa:2048 -nodes -keyout /private/gossip/ca.key -out /private/gossip/ca.crt \
     -days 365 -subj '/CN=Gossip Acceptance CA' \
     -addext 'basicConstraints=critical,CA:TRUE' \
     -addext 'keyUsage=critical,keyCertSign,cRLSign'
   chmod 600 /private/gossip/ca.key
   ```

2. **Coordinator Server Key and Certificate with SAN and serverAuth EKU (`home.key`, `home.crt`):**
   *(Replace `HOME_HOST` with the actual DNS hostname and `HOST_A_IP` with the LAN IP address of Host A)*
   ```sh
   openssl req -newkey rsa:2048 -nodes -keyout /private/gossip/home.key -out /private/gossip/home.csr \
     -subj '/CN=home'
   printf 'subjectAltName=DNS:HOME_HOST,IP:HOST_A_IP\nextendedKeyUsage=serverAuth\nkeyUsage=digitalSignature,keyEncipherment\n' > /private/gossip/home.ext
   openssl x509 -req -in /private/gossip/home.csr -CA /private/gossip/ca.crt -CAkey /private/gossip/ca.key \
     -CAcreateserial -out /private/gossip/home.crt -days 30 -sha256 -extfile /private/gossip/home.ext
   chmod 600 /private/gossip/home.key
   ```

3. **Owner Client Key and Certificate with clientAuth EKU (`owner.key`, `owner.crt`):**
   ```sh
   openssl req -newkey rsa:2048 -nodes -keyout /private/gossip/owner.key -out /private/gossip/owner.csr \
     -subj '/CN=owner'
   printf 'extendedKeyUsage=clientAuth\nkeyUsage=digitalSignature,keyEncipherment\n' > /private/gossip/owner.ext
   openssl x509 -req -in /private/gossip/owner.csr -CA /private/gossip/ca.crt -CAkey /private/gossip/ca.key \
     -CAcreateserial -out /private/gossip/owner.crt -days 30 -sha256 -extfile /private/gossip/owner.ext
   chmod 600 /private/gossip/owner.key
   ```

4. **Worker Client Key and Certificate with clientAuth EKU (`worker.key`, `worker.crt`):**
   *(Generate on Host B, or generate on Host A and transfer `worker.key` and `worker.crt` securely to Host B)*
   ```sh
   openssl req -newkey rsa:2048 -nodes -keyout /private/gossip/worker.key -out /private/gossip/worker.csr \
     -subj '/CN=worker'
   printf 'extendedKeyUsage=clientAuth\nkeyUsage=digitalSignature,keyEncipherment\n' > /private/gossip/worker.ext
   openssl x509 -req -in /private/gossip/worker.csr -CA /private/gossip/ca.crt -CAkey /private/gossip/ca.key \
     -CAcreateserial -out /private/gossip/worker.crt -days 30 -sha256 -extfile /private/gossip/worker.ext
   chmod 600 /private/gossip/worker.key
   ```

5. **Ungranted Test Client Key and Certificate with clientAuth EKU (`ungranted.key`, `ungranted.crt`):**
   ```sh
   openssl req -newkey rsa:2048 -nodes -keyout /private/gossip/ungranted.key -out /private/gossip/ungranted.csr \
     -subj '/CN=ungranted'
   printf 'extendedKeyUsage=clientAuth\nkeyUsage=digitalSignature,keyEncipherment\n' > /private/gossip/ungranted.ext
   openssl x509 -req -in /private/gossip/ungranted.csr -CA /private/gossip/ca.crt -CAkey /private/gossip/ca.key \
     -CAcreateserial -out /private/gossip/ungranted.crt -days 30 -sha256 -extfile /private/gossip/ungranted.ext
   chmod 600 /private/gossip/ungranted.key
   ```

## Set up the single home

On Host A, create a private test directory and initialize a disposable project in the database used by the coordinator. Keep the server and dashboard running in separate terminals.

```sh
mkdir -p /private/gossip && chmod 700 /private/gossip
gossip coord-init --db /private/gossip/home.db --name two-host-check --owner acceptance-owner --credential-file /private/gossip/owner.json
gossip coord-access --db /private/gossip/home.db --credential-file /private/gossip/owner.json --action grant --certificate /private/gossip/owner.crt
```

Start the authoritative home listener. Choose the LAN address or `0.0.0.0` only when the host firewall restricts access to the intended network. The server certificate must match the URL Host B will use. Keep the owner credential on Host A and start its loopback dashboard: `gossip coord-ui --db /private/gossip/home.db --port 8081`.

```sh
gossip coord-serve --db /private/gossip/home.db --listen 0.0.0.0:8443 --tls-cert /private/gossip/home.crt --tls-key /private/gossip/home.key --client-ca /private/gossip/ca.crt
```

### Critical Step: Enable Remote Access (`coord-remote`)

**IMPORTANT**: Because `remote_access_control.enabled=0` defaults to disabled in SQLite on new and upgraded databases, all remote network RPC and HTTPS requests will be rejected with `HTTP 503 Service Unavailable` out of the box.

In a separate terminal on Host A, verify the initial default-off status and explicitly enable remote access:

```sh
# Verify kill switch is active (disabled by default)
gossip coord-remote --db /private/gossip/home.db --action status
# Expected output: "disabled (kill switch active)"

# Enable remote coordination via local CLI
gossip coord-remote --db /private/gossip/home.db --action enable
# Expected output: "enabled"
```

### Reverse Proxy Setup with `coord-frp` (Optional)

If Host A is behind NAT or a firewall and reachable only through a reverse proxy, use the built-in `coord-frp` generator to produce a secure, hardened `frpc.toml` configuration forwarding TCP to the mTLS listener (`:8443`). Never expose the unauthenticated HTTP dashboard over FRP:

```sh
gossip coord-frp --server FRP_SERVER_HOST --server-port 7000 --token FRP_AUTH_TOKEN \
  --local-port 8443 --remote-port 8443 --config-file /private/gossip/frpc.toml
chmod 600 /private/gossip/frpc.toml
frpc -c /private/gossip/frpc.toml &
```

## Enroll the remote worker

On Host A, create the worker membership and grant its client certificate to this project atomically. The command writes the worker credential locally with restrictive permissions; transfer that file securely to Host B.

```sh
gossip coord-add --remote https://HOME_HOST:8443 --tls-ca /private/gossip/ca.crt --tls-cert /private/gossip/owner.crt --tls-key /private/gossip/owner.key --owner-credential /private/gossip/owner.json --name acceptance-worker --role worker --client-certificate /private/gossip/worker.crt --credential-file /private/gossip/worker.json
```

On Host B, protect the received credential and configure the coding-agent MCP host to start its project adapter with its working directory set to the registered checkout when testing source capture. (The adapter uses stdio, so the coding-agent MCP host should launch it.)

```sh
chmod 600 /private/gossip/worker.json
gossip coord-mcp --remote https://HOME_HOST:8443 --tls-ca /private/gossip/ca.crt --tls-cert /private/gossip/worker.crt --tls-key /private/gossip/worker.key --credential-file /private/gossip/worker.json
```

## Exercise access control and collaboration

1. In the owner dashboard on Host A, connect the owner credential, create a repository under Workspaces, then create a plan and a research or implementation task. During planning, select either **Full code review** or **Markdown report with UML class and sequence diagrams**. The MCP task tool must ask the user to choose when the request did not specify a review type.
2. In the worker's coding-agent host on Host B, connect the worker MCP adapter. Confirm the owner sees an authenticated heartbeat for that project. Register the Host B checkout as a worker-owned workspace; verify the owner can see its name and revision but not its absolute local path.
3. Have the worker acknowledge the plan, claim the task, exchange a project message, and submit the planned review:
   - **For Full Code Review (`full_code`)**:
     1. Worker captures diff from registered checkout: `gossip_submit(operation="capture_diff", task_id="...", attempt=1, request_id="candidate-1")`.
     2. Worker requests review and submits report linking the candidate digest.
   - **For Diagram Review (`diagram_report`)**:
     Worker submits a Markdown report containing all 6 mandatory sections and two Mermaid blocks:
     1. `## Review summary`
     2. `## Scope and evidence`
     3. `## UML class diagram` (with ````mermaid\nclassDiagram...````)
     4. `## Sequence diagram` (with ````mermaid\nsequenceDiagram...````)
     5. `## Findings`
     6. `## Coverage and limitations`
   Confirm the owner receives the review event, reads the exact evidence, and accepts or requests rework.
4. Create a second disposable project on Host A and grant the worker certificate to that project without enrolling the worker there:

   ```sh
   gossip coord-init --db /private/gossip/home.db --name unrelated-check --owner unrelated-owner --credential-file /private/gossip/unrelated-owner.json
   gossip coord-access --db /private/gossip/home.db --credential-file /private/gossip/unrelated-owner.json --action grant --certificate /private/gossip/worker.crt
   ```

   From Host B, use the protected worker credential with the second project ID in a request to `/v1/call` (`identity.read`); the certificate gate should pass and the project credential should be rejected with 401. Keep the token out of command arguments and logs with a private temporary curl config:

   ```sh
   umask 077
   auth_cfg=$(mktemp)
   trap 'rm -f "$auth_cfg"' EXIT
   python3 - /private/gossip/worker.json > "$auth_cfg" <<'PY'
   import json, sys
   with open(sys.argv[1], encoding="utf-8") as source:
       credential = json.load(source)
   print("header = " + json.dumps("Authorization: Bearer " + credential["token"]))
   PY
   curl --config "$auth_cfg" --cacert /private/gossip/ca.crt \
     --cert /private/gossip/worker.crt --key /private/gossip/worker.key \
     -H 'Content-Type: application/json' \
     --data '{"operation":"identity.read","project":"SECOND_PROJECT_ID"}' \
     --write-out '\nHTTP %{http_code}\n' https://HOME_HOST:8443/v1/call
   rm -f "$auth_cfg"
   trap - EXIT
   ```

   Expect HTTP 401 and an unauthorized project credential result. Separately, use the valid-but-ungranted test client certificate with the enrolled worker credential against the first project; expect 403 from the certificate-grant gate. A certificate grant alone must never create project membership.
5. Revoke the worker membership from the owner dashboard or local CLI:
   ```sh
   gossip coord-access --db /private/gossip/home.db --credential-file /private/gossip/owner.json --action revoke-principal --principal PRINCIPAL_ID
   ```
   Verify the next authoritative project request is rejected (HTTP 401), the worker cannot reconnect with the old credential, and its active task attempt is immediately fenced (`status='ready', owner=NULL, lease_until=0`).
   Re-enroll a fresh disposable worker credential, then revoke only the worker certificate grant:
   ```sh
   gossip coord-access --db /private/gossip/home.db --credential-file /private/gossip/owner.json --action revoke --certificate /private/gossip/worker.crt
   ```
   Verify that TLS RPC requests are rejected immediately with HTTP 403 Forbidden while the local project credential remains valid. Record the HTTP/MCP status and message for both cases.
6. **Remote Kill Switch Verification**:
   Verify the local-only remote access kill switch dynamically cuts off remote traffic while preserving local operations:
   - On Host A, disable remote access:
     ```sh
     gossip coord-remote --db /private/gossip/home.db --action disable
     ```
   - On Host B, immediately execute any request to `/v1/call`:
     Verify that the request is rejected immediately with `HTTP 503 Service Unavailable` ("remote coordination is disabled by the local kill switch") before any payload parsing.
   - On Host B, attempt to mutate or query the kill switch remotely:
     ```sh
     curl --cacert /private/gossip/ca.crt --cert /private/gossip/worker.crt --key /private/gossip/worker.key \
       -H 'Content-Type: application/json' \
       --data '{"operation":"remote.access.set","enabled":true}' \
       --write-out '\nHTTP %{http_code}\n' https://HOME_HOST:8443/v1/call
     ```
     Verify that remote RPC receives HTTP 503 (or HTTP 401 Unauthorized); remote tampering is strictly impossible.
   - On Host A, confirm local operations (loopback Web dashboard at `http://127.0.0.1:8081` and direct CLI/DB operations) continue unhindered.
   - On Host A, re-enable remote access:
     ```sh
     gossip coord-remote --db /private/gossip/home.db --action enable
     ```
   - On Host B, re-run the request to `/v1/call`:
     Verify that normal mTLS RPC operations immediately resume with HTTP 200.

## Exercise reconnect and restart

1. With the worker connected, stop Host A's coordinator. Confirm the worker cannot claim or renew a task while the home is unavailable. If the adapter has an outbox, queue one supported message or review request and confirm it is visibly pending rather than delivered.
2. Restart `coord-serve` on Host A against the same database. Restart the worker's `coord-mcp` adapter on Host B. Confirm presence returns after MCP initialization, queued work is delivered once, and a retry does not duplicate it.
3. Repeat one read and one write after the restart. Confirm the project ID and home database remain the same and an unrelated project's data is still inaccessible.

## Acceptance record

Record evidence with secret values removed. Complete this table during external hardware execution:

| # | Field / Check | Details & Commands Executed | Expected Outcome | Observed Outcome | Status (PASS/FAIL) |
|---|---|---|---|---|---|
| 1 | Execution Date & Time | | YYYY-MM-DDTHH:MM:SSZ | | |
| 2 | Tester Name & Role | | Authorized release engineer | | |
| 3 | Coordinator Build Git Commit | `git rev-parse HEAD` on Host A | Valid commit hash | | |
| 4 | Host A (Coordinator) OS & Hardware | OS, arch, CPU, memory | Linux / macOS / Windows hardware | | |
| 5 | Host B (Worker) OS & Hardware | OS, arch, CPU, memory | Distinct physical computer | | |
| 6 | Network Route Type & Topology | Direct LAN or FRP reverse proxy | Independent physical network connection | | |
| 7 | Server Certificate & SAN Verification | `openssl x509 -noout -text -in home.crt` | SAN matches Host B dial target | | |
| 8 | Kill Switch Default-Off Verification | `curl -k https://127.0.0.1:8443/v1/call` immediately after `coord-serve` | HTTP 503 Service Unavailable | | |
| 9 | Kill Switch Local Enablement | `gossip coord-remote --action enable --db ...` | Status transitions to "enabled" | | |
| 10 | mTLS RPC Handshake | Worker connects via `coord-mcp` | HTTP 200 / MCP tools list | | |
| 11 | Workspace Registration & Path Privacy | `gossip_workspace(operation="register_git")` | Local path masked for non-owners | | |
| 12 | Plan Acknowledgment Fencing | Task claim before `AcknowledgePlan` | Rejected with ErrConflict (409) | | |
| 13 | Structured Peer Messaging | Direct thread message between workers | Private to peers; hidden from owner | | |
| 14 | Diagram Review Submission | Submit Markdown report with UML diagrams | Validated with 6 sections + 2 diagrams | | |
| 15 | Full-Code Review Submission | `gossip_submit(operation="capture_diff")` | Unified diff pinned to commit baseline | | |
| 16 | Independent Review Acceptance | Owner accepts report; author self-review | Accepted by owner; author rejected (401) | | |
| 17 | Dependency DAG Cascade | Check dependent task status after prerequisite accept | Automatically unblocks (ready) | | |
| 18 | Wrong-Project Credential Isolation | Project 1 token against Project 2 | HTTP 401 Unauthorized | | |
| 19 | Missing Certificate Grant Isolation | Ungranted cert against Project 1 | HTTP 403 Forbidden | | |
| 20 | Principal Revocation & Fencing | `coord-access --action revoke-principal` | Active task reset to ready; token 401 | | |
| 21 | Certificate Grant Revocation | `coord-access --action revoke --certificate` | TLS RPC HTTP 403; local token valid | | |
| 22 | Remote Kill Switch Cutoff | `coord-remote --action disable --db ...` | Immediate HTTP 503 from Host B | | |
| 23 | Remote Kill Switch Tamper Resistance | `remote.access.set` over TLS RPC | HTTP 503 / HTTP 401 (rejected) | | |
| 24 | Local Resilience During Cutoff | Loopback HTTP dashboard / local socket | Operations continue with HTTP 200 | | |
| 25 | Kill Switch Re-enablement | `coord-remote --action enable --db ...` | Immediate HTTP 200 resumption | | |
| 26 | Coordinator Restart Recovery | Stop and restart `coord-serve` on same DB | In-flight leases, tasks, messages intact | | |
| 27 | Outbox Retry & Deduplication | Enqueue offline mutations; restart and drain | Delivered exactly once; no duplicate tasks | | |
| 28 | **Open-Gate Status Attestation** | Physical two-host verification statement | **OPEN** (pending physical execution) or **CLOSED** | | |

P4 is complete only after this procedure succeeds between separate physical hosts and the completed record is attached to the release review. The automated mutual-TLS and FRP tests remain useful prerequisites, not substitutes for this run.
